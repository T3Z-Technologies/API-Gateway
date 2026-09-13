package studio

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/services"
)

type firestoreStore struct{ client *firestore.Client }
type firestoreTransaction struct {
	client      *firestore.Client
	transaction *firestore.Transaction
}

func storageError(err error) error {
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

func (store *firestoreStore) Get(ctx context.Context, path string) (Document, error) {
	snapshot, err := store.client.Doc(path).Get(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	return snapshot.Data(), nil
}

func (store *firestoreStore) List(ctx context.Context, specification Query) ([]Record, error) {
	var query firestore.Query
	if specification.Group {
		query = store.client.CollectionGroup(specification.Collection).Query
	} else {
		query = store.client.Collection(specification.Collection).Query
	}
	direction := firestore.Asc
	if specification.Desc {
		direction = firestore.Desc
	}
	order := specification.Order
	if order == "" {
		order = firestore.DocumentID
	}
	query = query.OrderBy(order, direction)
	maximum := specification.Limit
	if maximum <= 0 || maximum > 500 {
		maximum = 500
	}
	query = query.Limit(maximum)
	if specification.After != "" {
		if order != firestore.DocumentID || specification.Group {
			return nil, fail(400, "Unsupported cursor.")
		}
		query = query.StartAfter(store.client.Collection(specification.Collection).Doc(specification.After))
	}
	documents := query.Documents(ctx)
	defer documents.Stop()
	records := make([]Record, 0)
	for {
		snapshot, err := documents.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, storageError(err)
		}
		path := snapshot.Ref.Path
		if pieces := strings.SplitN(path, "/documents/", 2); len(pieces) == 2 {
			path = pieces[1]
		}
		records = append(records, Record{ID: snapshot.Ref.ID, Path: path, Data: snapshot.Data()})
	}
	return records, nil
}

func (store *firestoreStore) Set(ctx context.Context, path string, data Document, merge bool) error {
	var err error
	if merge {
		_, err = store.client.Doc(path).Set(ctx, data, firestore.MergeAll)
	} else {
		_, err = store.client.Doc(path).Set(ctx, data)
	}
	return storageError(err)
}

func (store *firestoreStore) Delete(ctx context.Context, path string) error {
	_, err := store.client.Doc(path).Delete(ctx)
	return storageError(err)
}

func (store *firestoreStore) Transact(ctx context.Context, action func(Transaction) error) error {
	return store.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		return action(&firestoreTransaction{client: store.client, transaction: transaction})
	})
}

func (transaction *firestoreTransaction) Get(path string) (Document, error) {
	snapshot, err := transaction.transaction.Get(transaction.client.Doc(path))
	if err != nil {
		return nil, storageError(err)
	}
	return snapshot.Data(), nil
}

func (transaction *firestoreTransaction) Set(path string, data Document, merge bool) error {
	if merge {
		return transaction.transaction.Set(transaction.client.Doc(path), data, firestore.MergeAll)
	}
	return transaction.transaction.Set(transaction.client.Doc(path), data)
}

func (transaction *firestoreTransaction) Delete(path string) error {
	return transaction.transaction.Delete(transaction.client.Doc(path))
}

type firebaseIdentity struct {
	auth     *auth.Client
	login    *services.FirebaseService
	duration time.Duration
}

func (identity *firebaseIdentity) Login(ctx context.Context, email, password string) (string, string, error) {
	result, err := identity.login.SignInWithPasswordContext(ctx, email, password)
	if err != nil {
		return "", "", err
	}
	decoded, err := identity.auth.VerifyIDTokenAndCheckRevoked(ctx, result.AccessToken)
	if err != nil {
		return "", "", err
	}
	session, err := identity.auth.SessionCookie(ctx, result.AccessToken, identity.duration)
	return session, decoded.UID, err
}

func (identity *firebaseIdentity) Verify(ctx context.Context, session string) (string, error) {
	decoded, err := identity.auth.VerifySessionCookieAndCheckRevoked(ctx, session)
	if err != nil {
		return "", err
	}
	return decoded.UID, nil
}

func (identity *firebaseIdentity) CreateUser(ctx context.Context, email, password, name string) (string, error) {
	params := (&auth.UserToCreate{}).Email(email).Password(password)
	if name != "" {
		params.DisplayName(name)
	}
	record, err := identity.auth.CreateUser(ctx, params)
	if err != nil {
		if auth.IsEmailAlreadyExists(err) {
			return "", fail(409, "An account with this email already exists.")
		}
		return "", err
	}
	return record.UID, nil
}

func (identity *firebaseIdentity) DeleteUser(ctx context.Context, uid string) error {
	err := identity.auth.DeleteUser(ctx, uid)
	if auth.IsUserNotFound(err) {
		return nil
	}
	return err
}

func NewFirebaseHandler(ctx context.Context, cfg *config.Config, db *database.DB) (*Handler, func() error, error) {
	if cfg.FirebaseAPIKey == "" || (cfg.FirebaseCredentials == "" && os.Getenv("FIRESTORE_EMULATOR_HOST") == "") {
		return nil, nil, errors.New("configure T3Z_FIREBASE_API_KEY and GOOGLE_APPLICATION_CREDENTIALS for the Studio API")
	}
	if cfg.StudioSessionHours < 1 || cfg.StudioSessionHours > 336 || (cfg.StudioCookieSameSite != "lax" && cfg.StudioCookieSameSite != "none") || (cfg.StudioCookieSameSite == "none" && !cfg.StudioCookieSecure) {
		return nil, nil, errors.New("invalid Studio session cookie configuration")
	}
	options := make([]option.ClientOption, 0)
	if cfg.FirebaseCredentials != "" {
		options = append(options, option.WithCredentialsFile(cfg.FirebaseCredentials))
	}
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: cfg.FirebaseProjectID}, options...)
	if err != nil {
		return nil, nil, err
	}
	authClient, err := app.Auth(ctx)
	if err != nil {
		return nil, nil, err
	}
	client, err := app.Firestore(ctx)
	if err != nil {
		return nil, nil, err
	}
	limits, err := newSQLiteLimits(db)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	identity := &firebaseIdentity{auth: authClient, login: services.NewFirebaseService(cfg), duration: time.Duration(cfg.StudioSessionHours) * time.Hour}
	return NewHandler(cfg, &firestoreStore{client: client}, identity, limits), client.Close, nil
}
