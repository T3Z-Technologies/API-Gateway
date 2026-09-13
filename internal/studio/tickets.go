package studio

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ticketInput struct {
	Action          string `json:"action"`
	OwnerUID        string `json:"ownerUid"`
	TicketID        string `json:"ticketId"`
	ExpectedVersion *int64 `json:"expectedVersion"`
	Subject         string `json:"subject"`
	Message         string `json:"message"`
	Category        string `json:"category"`
	Status          string `json:"status"`
}

func (handler *Handler) listTickets(writer http.ResponseWriter, request *http.Request) {
	handler.listOwned(writer, request, "tickets", "updatedAt", 100)
}

func (handler *Handler) allTickets(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	handler.list(writer, request, Query{Collection: "tickets", Group: true, Order: "updatedAt", Desc: true, Limit: 500})
}

func (handler *Handler) ticketMessages(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	identifier := chi.URLParam(request, "id")
	if !validID(identifier) {
		writeError(writer, fail(400, "Invalid ticket."))
		return
	}
	path := "users/" + uid + "/tickets/" + identifier
	if _, err := handler.store.Get(request.Context(), path); err != nil {
		writeError(writer, err)
		return
	}
	handler.list(writer, request, Query{Collection: path + "/messages", Order: "createdAt", Limit: 100})
}

func optionalDocument(transaction Transaction, path string) (Document, error) {
	data, err := transaction.Get(path)
	if errors.Is(err, ErrNotFound) {
		return Document{}, nil
	}
	return data, err
}

func windowCount(data Document, prefix string, now int64) (int64, int) {
	started := int64(number(data, prefix+"WindowStartedAt"))
	if started <= now-int64(10*time.Minute/time.Millisecond) {
		return now, 0
	}
	return started, int(number(data, prefix+"WindowCount"))
}

func (handler *Handler) ticketCommand(writer http.ResponseWriter, request *http.Request) {
	var input ticketInput
	if err := readJSON(writer, request, &input, 64*1024); err != nil {
		writeError(writer, err)
		return
	}
	caller := current(request)
	uid := caller.UID
	if caller.Role == "admin" {
		uid = input.OwnerUID
	} else if input.OwnerUID != "" && input.OwnerUID != uid {
		writeError(writer, fail(403, "This workspace is not accessible."))
		return
	}
	if input.Action == "create" {
		if caller.Role != "client" {
			writeError(writer, fail(403, "Client access is required."))
			return
		}
		handler.createTicket(writer, request, input)
		return
	}
	if !validID(uid) || !validID(input.TicketID) || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		writeError(writer, fail(400, "Invalid ticket reference or version."))
		return
	}
	profile, err := handler.store.Get(request.Context(), "users/"+uid)
	if err != nil {
		writeError(writer, err)
		return
	}
	if text(profile, "role") != "client" {
		writeError(writer, fail(404, "Client ticket not found."))
		return
	}
	if input.Action == "status" {
		if caller.Role != "admin" {
			writeError(writer, fail(403, "Admin access is required."))
			return
		}
		if err := enumField(Document{"status": input.Status}, "status", "open", "in_progress", "resolved"); err != nil {
			writeError(writer, err)
			return
		}
	} else if input.Action == "message" {
		input.Message = strings.TrimSpace(input.Message)
		if input.Message == "" || len(input.Message) > 4000 || strings.ContainsRune(input.Message, 0) {
			writeError(writer, fail(400, "Write a message of at most 4000 characters."))
			return
		}
		if caller.Role == "admin" && input.Status != "" {
			if err := enumField(Document{"status": input.Status}, "status", "open", "in_progress", "resolved"); err != nil {
				writeError(writer, err)
				return
			}
		}
	} else {
		writeError(writer, fail(400, "Unknown ticket action."))
		return
	}
	path := "users/" + uid + "/tickets/" + input.TicketID
	limiterPath := "_ticketLimits/" + caller.UID
	messageID := uuid.NewString()
	now := time.Now().UnixMilli()
	nextVersion := *input.ExpectedVersion + 1
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		data, err := transaction.Get(path)
		if err != nil {
			return err
		}
		if int64(number(data, "version")) != *input.ExpectedVersion {
			return fail(409, "This ticket changed. Review the latest conversation and try again.")
		}
		patch := Document{"version": nextVersion, "revision": uuid.NewString(), "updatedAt": now, "clientUnread": caller.Role == "admin", "adminUnread": caller.Role == "client"}
		if input.Action == "status" {
			patch["status"] = input.Status
			return transaction.Set(path, patch, true)
		}
		count := int(number(data, "messageCount"))
		if count >= 100 {
			return fail(409, "This conversation has reached its message limit.")
		}
		if caller.Role == "client" {
			limiter, err := optionalDocument(transaction, limiterPath)
			if err != nil {
				return err
			}
			started, attempts := windowCount(limiter, "message", now)
			if attempts >= 30 {
				return fail(429, "Too many messages. Try again in a few minutes.")
			}
			if err := transaction.Set(limiterPath, Document{"messageWindowStartedAt": started, "messageWindowCount": attempts + 1, "updatedAt": now}, true); err != nil {
				return err
			}
		}
		state := text(data, "status")
		if caller.Role == "client" && state == "resolved" {
			state = "open"
		}
		if caller.Role == "admin" && input.Status != "" {
			state = input.Status
		}
		patch["status"], patch["messageCount"] = state, count+1
		patch["lastMessageId"], patch["lastMessageSender"], patch["lastMessageAt"] = messageID, caller.Role, now
		if caller.Role == "admin" && text(data, "response") == "" {
			patch["response"], patch["responseAt"] = input.Message, now
		}
		if err := transaction.Set(path+"/messages/"+messageID, Document{"sender": caller.Role, "senderUid": caller.UID, "body": input.Message, "createdAt": now}, false); err != nil {
			return err
		}
		return transaction.Set(path, patch, true)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true, "version": nextVersion})
}

func (handler *Handler) createTicket(writer http.ResponseWriter, request *http.Request, input ticketInput) {
	uid := current(request).UID
	data := Document{"subject": input.Subject, "message": input.Message, "category": input.Category}
	if err := stringField(data, "subject", 160, true); err != nil {
		writeError(writer, err)
		return
	}
	if err := stringField(data, "message", 10000, true); err != nil {
		writeError(writer, err)
		return
	}
	if err := enumField(data, "category", "bug", "prompt", "question", "other"); err != nil {
		writeError(writer, err)
		return
	}
	limiterPath := "_ticketLimits/" + uid
	limiter, err := handler.store.Get(request.Context(), limiterPath)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeError(writer, err)
		return
	}
	seededCount := 0
	if _, exists := limiter["lifetimeTicketCount"]; !exists {
		existing, err := handler.store.List(request.Context(), Query{Collection: "users/" + uid + "/tickets", Limit: 250})
		if err != nil {
			writeError(writer, err)
			return
		}
		seededCount = len(existing)
	}
	identifier := uuid.NewString()
	now := time.Now().UnixMilli()
	data["status"], data["response"], data["clientUnread"], data["adminUnread"] = "open", "", false, true
	data["messageCount"], data["version"], data["revision"] = 0, 0, uuid.NewString()
	data["createdAt"], data["updatedAt"] = now, now
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		limiter, err := optionalDocument(transaction, limiterPath)
		if err != nil {
			return err
		}
		started, attempts := windowCount(limiter, "create", now)
		lifetime := seededCount
		if _, exists := limiter["lifetimeTicketCount"]; exists {
			lifetime = int(number(limiter, "lifetimeTicketCount"))
		}
		if attempts >= 5 {
			return fail(429, "Too many new tickets. Try again in a few minutes.")
		}
		if lifetime >= 250 {
			return fail(409, "This workspace has reached its ticket limit.")
		}
		if err := transaction.Set(limiterPath, Document{"createWindowStartedAt": started, "createWindowCount": attempts + 1, "lifetimeTicketCount": lifetime + 1, "updatedAt": now}, true); err != nil {
			return err
		}
		return transaction.Set("users/"+uid+"/tickets/"+identifier, data, false)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 201, Document{"ok": true, "id": identifier})
}

func (handler *Handler) readTickets(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	var input struct {
		Items []struct {
			ID      string `json:"id"`
			Version *int64 `json:"version"`
		} `json:"items"`
	}
	if err := readJSON(writer, request, &input, 32*1024); err != nil {
		writeError(writer, err)
		return
	}
	if len(input.Items) > 100 {
		writeError(writer, fail(400, "Too many tickets."))
		return
	}
	field := "clientUnread"
	if current(request).Role == "admin" {
		field = "adminUnread"
	}
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		paths := make([]string, 0, len(input.Items))
		for _, item := range input.Items {
			if !validID(item.ID) || item.Version == nil || *item.Version < 0 {
				return fail(400, "Invalid ticket reference.")
			}
			path := "users/" + uid + "/tickets/" + item.ID
			data, err := transaction.Get(path)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if data[field] == true && int64(number(data, "version")) == *item.Version {
				paths = append(paths, path)
			}
		}
		for _, path := range paths {
			if err := transaction.Set(path, Document{field: false}, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}
