package studio

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

func (handler *Handler) createOrder(writer http.ResponseWriter, request *http.Request) {
	caller := current(request)
	if caller.Role != "client" {
		writeError(writer, fail(403, "Client access is required."))
		return
	}
	if handler.cfg.RazorpayKeyID == "" || handler.cfg.RazorpayKeySecret == "" {
		writeError(writer, fail(503, "Payments are not configured."))
		return
	}
	var input struct {
		PackID   string `json:"packId"`
		Kind     string `json:"kind"`
		Quantity int64  `json:"quantity"`
	}
	if err := readJSON(writer, request, &input, 4096); err != nil {
		writeError(writer, err)
		return
	}
	var amount int64
	kind, quantity := input.Kind, input.Quantity
	if input.PackID != "" {
		if kind != "" || quantity != 0 {
			writeError(writer, fail(400, "Select one purchase."))
			return
		}
		packs := map[string][2]int64{"pack-150": {150, 75000}, "pack-350": {350, 157500}, "pack-1000": {1000, 400000}}
		pack, exists := packs[input.PackID]
		if !exists {
			writeError(writer, fail(400, "Unknown credit pack."))
			return
		}
		kind, quantity, amount = "voice", pack[0], pack[1]
	} else {
		switch kind {
		case "subscription":
			if quantity != 1 {
				writeError(writer, fail(400, "Invalid subscription quantity."))
				return
			}
			amount = int64(math.Round(number(caller.Profile, "monthlyPrice") * 100))
		case "voice":
			if quantity != 100 && quantity != 300 && quantity != 600 {
				writeError(writer, fail(400, "Unknown voice pack."))
				return
			}
			amount = quantity * 500
		case "actions":
			if quantity != 500 && quantity != 1000 && quantity != 5000 {
				writeError(writer, fail(400, "Unknown action pack."))
				return
			}
			amount = quantity * 200
		default:
			writeError(writer, fail(400, "Unknown purchase."))
			return
		}
	}
	if amount <= 0 || amount > 100000000 {
		writeError(writer, fail(400, "This purchase amount is not available."))
		return
	}
	if err := handler.limits.Take(request.Context(), "orders:"+caller.UID, 10, time.Minute); err != nil {
		writeError(writer, err)
		return
	}
	encoded, _ := json.Marshal(Document{"amount": amount, "currency": "INR", "receipt": uuid.NewString(), "notes": Document{"uid": caller.UID, "kind": kind, "quantity": strconv.FormatInt(quantity, 10), "packId": input.PackID}})
	upstream, err := http.NewRequestWithContext(request.Context(), http.MethodPost, "https://api.razorpay.com/v1/orders", bytes.NewReader(encoded))
	if err != nil {
		writeError(writer, err)
		return
	}
	upstream.SetBasicAuth(handler.cfg.RazorpayKeyID, handler.cfg.RazorpayKeySecret)
	upstream.Header.Set("Content-Type", "application/json")
	response, err := handler.httpClient.Do(upstream)
	if err != nil {
		writeError(writer, fail(502, "Could not reach the payment service."))
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 {
		writeError(writer, fail(502, "Could not create the payment order."))
		return
	}
	var order struct {
		ID       string `json:"id"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&order); err != nil || !validID(order.ID) || order.Amount != amount || order.Currency != "INR" {
		writeError(writer, fail(502, "The payment service returned an invalid order."))
		return
	}
	if err := handler.store.Set(request.Context(), "_paymentOrders/"+order.ID, Document{"uid": caller.UID, "kind": kind, "quantity": quantity, "amount": amount, "currency": "INR", "credited": false, "createdAt": time.Now().UnixMilli()}, false); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"orderId": order.ID, "amount": amount, "currency": "INR", "keyId": handler.cfg.RazorpayKeyID})
}

func signedBody(writer http.ResponseWriter, request *http.Request, header, secret string) ([]byte, error) {
	if secret == "" {
		return nil, fail(503, "This webhook is not configured.")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 64*1024))
	if err != nil {
		return nil, fail(413, "The webhook is too large.")
	}
	provided, err := hex.DecodeString(request.Header.Get(header))
	signature := hmac.New(sha256.New, []byte(secret))
	_, _ = signature.Write(raw)
	if err != nil || !hmac.Equal(provided, signature.Sum(nil)) {
		return nil, fail(401, "Invalid webhook signature.")
	}
	return raw, nil
}

func (handler *Handler) paymentWebhook(writer http.ResponseWriter, request *http.Request) {
	raw, err := signedBody(writer, request, "X-Razorpay-Signature", handler.cfg.RazorpayWebhookSecret)
	if err != nil {
		writeError(writer, err)
		return
	}
	var event struct {
		Event   string `json:"event"`
		Payload struct {
			Order struct {
				Entity struct {
					ID       string `json:"id"`
					Amount   int64  `json:"amount"`
					Currency string `json:"currency"`
					Status   string `json:"status"`
				} `json:"entity"`
			} `json:"order"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		writeError(writer, fail(400, "Invalid payment event."))
		return
	}
	if event.Event != "order.paid" {
		writeJSON(writer, 200, Document{"ok": true, "ignored": true})
		return
	}
	order := event.Payload.Order.Entity
	if !validID(order.ID) || order.Status != "paid" {
		writeError(writer, fail(400, "Invalid paid order."))
		return
	}
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		path := "_paymentOrders/" + order.ID
		payment, err := transaction.Get(path)
		if err != nil {
			return err
		}
		if int64(number(payment, "amount")) != order.Amount || text(payment, "currency") != order.Currency {
			return fail(400, "The paid amount does not match the order.")
		}
		if payment["credited"] == true {
			return nil
		}
		uid := text(payment, "uid")
		if !validID(uid) {
			return fail(400, "Invalid order owner.")
		}
		profile, err := transaction.Get("users/" + uid)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		patch := Document{"updatedAt": now}
		kind := text(payment, "kind")
		if kind == "voice" || kind == "actions" {
			meter, _ := profile[kind].(map[string]any)
			if meter == nil {
				meter = Document{"included": 0, "used": 0}
			}
			meter["addOn"] = number(meter, "addOn") + number(payment, "quantity")
			patch[kind] = meter
		} else if kind == "subscription" {
			patch["lastSubscriptionPaymentAt"], patch["lastSubscriptionOrderId"] = now, order.ID
		} else {
			return fail(400, "Unknown order kind.")
		}
		if err := transaction.Set("users/"+uid, patch, true); err != nil {
			return err
		}
		return transaction.Set(path, Document{"credited": true, "paidAt": now}, true)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) deduct(writer http.ResponseWriter, request *http.Request) {
	raw, err := signedBody(writer, request, "X-Webhook-Signature", handler.cfg.BillingWebhookSecret)
	if err != nil {
		writeError(writer, err)
		return
	}
	var input struct {
		EventID         string   `json:"eventId"`
		UID             string   `json:"uid"`
		DurationSeconds *float64 `json:"durationSeconds"`
		Minutes         *float64 `json:"minutes"`
		Cost            float64  `json:"cost"`
		CallerNumber    string   `json:"callerNumber"`
		Summary         string   `json:"summary"`
		Outcome         string   `json:"outcome"`
		RecordingURL    string   `json:"recordingUrl"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || !validID(input.UID) || !validID(input.EventID) {
		writeError(writer, fail(400, "A valid uid and stable eventId are required."))
		return
	}
	minutes := 0.0
	if input.DurationSeconds != nil {
		minutes = *input.DurationSeconds / 60
	}
	if input.Minutes != nil {
		if input.DurationSeconds != nil && math.Abs(minutes-*input.Minutes) > 0.001 {
			writeError(writer, fail(400, "Duration and minutes do not match."))
			return
		}
		minutes = *input.Minutes
	}
	if minutes <= 0 || minutes > 1440 || input.Cost < 0 || input.Cost > 1e9 || len(input.Summary) > 10000 || len(input.CallerNumber) > 100 || len(input.Outcome) > 1000 || !safeEndpoint(input.RecordingURL) {
		writeError(writer, fail(400, "Invalid call usage."))
		return
	}
	profilePath := "users/" + input.UID
	eventPath := "_billingEvents/" + hashKey(input.UID+":"+input.EventID)
	agents, err := handler.store.List(request.Context(), Query{Collection: profilePath + "/agents", Limit: 250})
	if err != nil {
		writeError(writer, err)
		return
	}
	result := Document{}
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		previous, err := transaction.Get(eventPath)
		if err == nil {
			result = Document{"ok": true, "remaining": previous["remaining"], "shutdown": previous["shutdown"], "duplicate": true}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		profile, err := transaction.Get(profilePath)
		if err != nil {
			return err
		}
		if text(profile, "role") != "client" {
			return fail(400, "Only client usage can be deducted.")
		}
		voice, _ := profile["voice"].(map[string]any)
		if voice == nil {
			voice = Document{"included": 0, "addOn": 0}
		}
		voice["used"] = number(voice, "used") + minutes
		remaining := number(voice, "included") + number(voice, "addOn") - number(voice, "used")
		paused := make([]string, 0)
		if remaining <= 0 {
			for _, agent := range agents {
				path := profilePath + "/agents/" + agent.ID
				data, err := transaction.Get(path)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if text(data, "status") == "active" {
					paused = append(paused, path)
				}
			}
		}
		now := time.Now().UnixMilli()
		if err := transaction.Set(profilePath, Document{"voice": voice, "updatedAt": now}, true); err != nil {
			return err
		}
		for _, path := range paused {
			if err := transaction.Set(path, Document{"status": "inactive", "updatedAt": now}, true); err != nil {
				return err
			}
		}
		log := Document{"timestamp": now, "durationSeconds": math.Round(minutes * 60), "callerNumber": input.CallerNumber, "summary": input.Summary, "outcome": input.Outcome, "costDeducted": input.Cost}
		if input.RecordingURL != "" {
			log["recordingUrl"] = input.RecordingURL
		}
		if err := transaction.Set(profilePath+"/call_logs/"+hashKey(input.EventID), log, false); err != nil {
			return err
		}
		result = Document{"ok": true, "remaining": remaining, "shutdown": remaining <= 0}
		return transaction.Set(eventPath, Document{"uid": input.UID, "eventId": input.EventID, "minutes": minutes, "remaining": remaining, "shutdown": remaining <= 0, "createdAt": now}, false)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, result)
}
