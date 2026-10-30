//go:build pgx

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// TestPostgresTeamProjectCreatesItsBank drives project creation against a real
// database: the bank "<team>::<name>" must exist in the team's own schema, be
// readable through the data plane with the team's key, and go away with the
// project. The in-memory suite pins the plane's decisions; this pins the row
// landing in the right schema, which is the part a fake cannot prove.
func TestPostgresTeamProjectCreatesItsBank(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the team project e2e test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	teamID := fmt.Sprintf("t_team_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{teamID, "admin"} {
			_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS \""+schema+"\" CASCADE")
		}
		_ = db.Close()
	})

	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:0"
	cfg.AdminAPIKey = "operator-secret"
	cfg.RunMigrationsOnStartup = true
	store := storage.NewMultiTenantPostgresStore(db, "public")
	application, err := New(ctx, cfg, Dependencies{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	admin := application.AdminHandler

	created := adminCall(t, admin, http.MethodPost, "/admin/v1/teams", fmt.Sprintf("{\"tenant_id\":%q}", teamID))
	if created.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", created.Code, created.Body.String())
	}
	var issued struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode team: %v", err)
	}

	project := adminCall(t, admin, http.MethodPost, "/admin/v1/teams/"+teamID+"/projects",
		`{"project_id":"web","name":"Web"}`)
	if project.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", project.Code, project.Body.String())
	}
	// The bank id is built from the project id; the name is only its label.
	bankID := teamID + "::web"

	// The row is in the team's schema, not the process schema and not admin.
	var name string
	if err := db.QueryRowContext(ctx,
		"SELECT name FROM \""+teamID+"\".banks WHERE bank_id = $1", bankID).Scan(&name); err != nil {
		t.Fatalf("the project's bank is not in the team's schema: %v", err)
	}
	if name != "Web" {
		t.Fatalf("bank name = %q, want the project's display name Web", name)
	}

	// The existing-bank guard sees it: a second project under that name in the
	// same team is a conflict, so creating a project never duplicates its bank.
	if again := adminCall(t, admin, http.MethodPost, "/admin/v1/teams/"+teamID+"/projects",
		`{"project_id":"web2","name":"Web"}`); again.Code != http.StatusConflict {
		t.Fatalf("create a second project onto the same bank = %d, want 409: %s", again.Code, again.Body.String())
	}
	// The team still owns exactly one bank, and one project row names it.
	var naming int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM \""+teamID+"\".banks WHERE bank_id = $1", bankID).Scan(&naming); err != nil {
		t.Fatalf("count the bank rows: %v", err)
	}
	if naming != 1 {
		t.Fatalf("the bank was created %d times, want once", naming)
	}

	// Another team may own a project of the same name: its bank id differs by
	// the team prefix, so the scoped unique constraints let both exist.
	otherTeam := fmt.Sprintf("t_team_b_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS "`+otherTeam+`" CASCADE`)
	})
	if created := adminCall(t, admin, http.MethodPost, "/admin/v1/teams",
		fmt.Sprintf(`{"tenant_id":%q}`, otherTeam)); created.Code != http.StatusCreated {
		t.Fatalf("create second team = %d: %s", created.Code, created.Body.String())
	}
	if second := adminCall(t, admin, http.MethodPost, "/admin/v1/teams/"+otherTeam+"/projects",
		`{"project_id":"web","name":"Web"}`); second.Code != http.StatusCreated {
		t.Fatalf("same-named project in another team = %d, want 201: %s", second.Code, second.Body.String())
	}
	var otherBank int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM \""+otherTeam+"\".banks WHERE bank_id = $1", otherTeam+"::web").Scan(&otherBank); err != nil {
		t.Fatalf("count the other team's bank: %v", err)
	}
	if otherBank != 1 {
		t.Fatalf("the other team's bank rows = %d, want 1", otherBank)
	}

	// The data plane agrees: the team's key lists the bank it owns.
	listed := dataPlaneCall(t, application.Handler, http.MethodGet, "/v1/default/banks", issued.Plaintext, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list banks with the team key = %d: %s", listed.Code, listed.Body.String())
	}
	var page struct {
		Banks []struct {
			BankID string `json:"bank_id"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode banks: %v", err)
	}
	found := false
	for _, bank := range page.Banks {
		if bank.BankID == bankID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the data plane does not list %s: %s", bankID, listed.Body.String())
	}

	// Deleting the project takes the bank with it.
	if deleted := adminCall(t, admin, http.MethodDelete, "/admin/v1/teams/"+teamID+"/projects/web",
		`{"confirm":"web","delete_banks":true}`); deleted.Code != http.StatusOK {
		t.Fatalf("delete project = %d: %s", deleted.Code, deleted.Body.String())
	}
	var remaining int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM \""+teamID+"\".banks WHERE bank_id = $1", bankID).Scan(&remaining); err != nil {
		t.Fatalf("count the bank: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("the bank survived its project: %d rows", remaining)
	}
}
