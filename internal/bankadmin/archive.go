package bankadmin

import (
	"context"
	"encoding/json"

	"github.com/studyzy/wenyuan/internal/storage"
)

// Archive is the serialized form of a whole bank.
type Archive struct {
	Bank    BankArchive    `json:"bank"`
	Aliases []AliasArchive `json:"aliases"`
}

// BankArchive is the archived bank profile.
type BankArchive struct {
	ID                    string `json:"bank_id"`
	Name                  string `json:"name"`
	DispositionSkepticism int    `json:"disposition_skepticism"`
	DispositionLiteralism int    `json:"disposition_literalism"`
	DispositionEmpathy    int    `json:"disposition_empathy"`
	ReflectMission        string `json:"reflect_mission"`
}

// AliasArchive is one archived alias.
type AliasArchive struct {
	Alias   string `json:"alias"`
	Primary bool   `json:"primary"`
}

// Exporter produces and consumes bank archives.
type Exporter struct {
	Store storage.Store
}

// Export serializes a bank and its aliases into an archive.
func (e Exporter) Export(ctx context.Context, bankID string) ([]byte, error) {
	bank, err := e.Store.GetBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	aliases, err := e.Store.ListAliases(ctx, bankID)
	if err != nil {
		return nil, err
	}
	archive := Archive{
		Bank: BankArchive{
			ID:                    bank.ID,
			Name:                  bank.Name,
			DispositionSkepticism: bank.DispositionSkepticism,
			DispositionLiteralism: bank.DispositionLiteralism,
			DispositionEmpathy:    bank.DispositionEmpathy,
			ReflectMission:        bank.ReflectMission,
		},
		Aliases: make([]AliasArchive, 0, len(aliases)),
	}
	for _, alias := range aliases {
		archive.Aliases = append(archive.Aliases, AliasArchive{Alias: alias.Alias, Primary: alias.Primary})
	}
	return json.Marshal(archive)
}

// Import re-creates a bank under the target identifier from an archive.
func (e Exporter) Import(ctx context.Context, data []byte, targetID string) error {
	var archive Archive
	if err := json.Unmarshal(data, &archive); err != nil {
		return err
	}
	if !storage.ValidBankID(targetID) {
		return storage.ErrInvalidBankID
	}
	if _, err := e.Store.CreateBank(ctx, storage.Bank{
		ID:                    targetID,
		Name:                  archive.Bank.Name,
		DispositionSkepticism: archive.Bank.DispositionSkepticism,
		DispositionLiteralism: archive.Bank.DispositionLiteralism,
		DispositionEmpathy:    archive.Bank.DispositionEmpathy,
		ReflectMission:        archive.Bank.ReflectMission,
	}); err != nil {
		return err
	}
	for _, alias := range archive.Aliases {
		if _, err := e.Store.AddAlias(ctx, targetID, alias.Alias, alias.Primary); err != nil {
			return err
		}
	}
	return nil
}
