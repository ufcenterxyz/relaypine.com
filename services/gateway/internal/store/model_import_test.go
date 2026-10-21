package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/go-sql-driver/mysql"
	"relaypine.com/gateway/internal/domain"
)

func importTestModel(id string) domain.Model {
	return domain.Model{ID: id, Name: "Import test", Provider: "anthropic", UpstreamModel: "upstream-test", BillingMode: domain.BillingUsageOnly, Enabled: true, MaxOutputTokens: 4096}
}

func TestCreateModelsValidatesWholeBatchBeforeDatabaseAccess(t *testing.T) {
	valid := importTestModel("valid")
	invalid := importTestModel("invalid")
	invalid.CostCacheWriteRate = 1
	for _, input := range [][]domain.Model{nil, {}, make([]domain.Model, MaxModelImportItems+1), {valid, valid}, {valid, {}}, {valid, invalid}} {
		// An uninitialized Store panics if any invalid batch reaches the database.
		if _, err := (&Store{}).CreateModels(context.Background(), input); !errors.Is(err, ErrInvalidModelBatch) {
			t.Fatalf("invalid batch reached storage: %v", err)
		}
	}
	for _, change := range []func(*domain.Model){
		func(m *domain.Model) { m.BillingMode = "unknown" },
		func(m *domain.Model) { m.BillingMode = "" },
		func(m *domain.Model) { m.MaxOutputTokens = 0 },
		func(m *domain.Model) { m.Provider = "unknown" },
	} {
		candidate := valid
		change(&candidate)
		if err := validateModel(candidate); err == nil {
			t.Fatal("invalid model bypassed the store validation boundary")
		}
	}
}

// This driver injects a non-duplicate SQL failure after a successful insert and
// a duplicate. It verifies transaction rollback without mutating shared schema
// or requiring database-wide privileges in integration environments.
type importFailureDriver struct{ state *importFailureState }
type importFailureState struct {
	attempted             []string
	committed, rolledBack bool
}
type importFailureConn struct{ state *importFailureState }
type importFailureTx struct{ state *importFailureState }
type importFailureStmt struct{ state *importFailureState }

func (d importFailureDriver) Open(string) (driver.Conn, error) {
	return &importFailureConn{state: d.state}, nil
}
func (c *importFailureConn) Prepare(string) (driver.Stmt, error) {
	return &importFailureStmt{state: c.state}, nil
}
func (c *importFailureConn) Close() error { return nil }
func (c *importFailureConn) Begin() (driver.Tx, error) {
	return &importFailureTx{state: c.state}, nil
}
func (tx *importFailureTx) Commit() error { tx.state.committed = true; return nil }
func (tx *importFailureTx) Rollback() error {
	tx.state.rolledBack = true
	return nil
}
func (s *importFailureStmt) Close() error  { return nil }
func (s *importFailureStmt) NumInput() int { return 3 }
func (s *importFailureStmt) Exec(args []driver.Value) (driver.Result, error) {
	id := args[0].(string)
	s.state.attempted = append(s.state.attempted, id)
	switch id {
	case "b-existing":
		return nil, &mysql.MySQLError{Number: 1062, Message: "duplicate fixture ID"}
	case "c-failure":
		return nil, &mysql.MySQLError{Number: 1366, Message: "injected storage failure"}
	default:
		return driver.RowsAffected(1), nil
	}
}
func (*importFailureStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("queries are not supported by this test driver")
}

var importFailureDriverID atomic.Uint64

func TestCreateModelsRollsBackOnNonDuplicateStorageError(t *testing.T) {
	state := &importFailureState{}
	name := fmt.Sprintf("model-import-failure-%d", importFailureDriverID.Add(1))
	sql.Register(name, importFailureDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{DB: db}
	result, err := s.CreateModels(context.Background(), []domain.Model{importTestModel("c-failure"), importTestModel("b-existing"), importTestModel("a-created")})
	var failure *mysql.MySQLError
	if !errors.As(err, &failure) || failure.Number != 1366 {
		t.Fatalf("non-duplicate failure was suppressed: %v", err)
	}
	if !state.rolledBack || state.committed || !reflect.DeepEqual(state.attempted, []string{"a-created", "b-existing", "c-failure"}) {
		t.Fatalf("batch was not inserted in stable order and rolled back: %+v", state)
	}
	if result.CreatedCount != 0 || result.SkippedCount != 0 || result.Results != nil {
		t.Fatal("failed transaction returned misleading partial results")
	}
}
