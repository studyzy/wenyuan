package app

import (
	"context"
	"errors"

	"github.com/studyzy/wenyuan/internal/storage"
)

// bankExistsInSchema answers whether a bank identifier is a bank of the schema
// the request resolved to.
//
// It is GetBank, not a listing or a count: GetBank reads the schema the context
// carries and returns storage.ErrBankNotFound for an id that is not there, so
// the answer is confined to the caller's own tenant and probing for a bank
// creates nothing. It is wired as httpx.RouterOptions.BankExists, where the only
// use is to let a whole-bank delete or replace reach a bank whose id carries a
// prefix this service no longer hands out (the `public::` banks an earlier
// client wrote into a tenant's schema). A bank that is absent — or that lives in
// another schema, which this context cannot see — is not a bank of the caller's,
// and keeps the missing-bank 404.
func bankExistsInSchema(store storage.Store) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, bankID string) (bool, error) {
		_, err := store.GetBank(ctx, bankID)
		if errors.Is(err, storage.ErrBankNotFound) {
			return false, nil
		}
		return err == nil, err
	}
}
