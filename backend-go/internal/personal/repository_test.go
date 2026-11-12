package personal

import (
	"context"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSaveInterestsBindsArraysAsSingleParameters(t *testing.T) {
	orm, err := gorm.Open(postgres.Open("postgres://localhost:1/unused_test?sslmode=disable"), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var statement string
	var parameters int
	if err := orm.Callback().Raw().After("gorm:raw").Register("capture_interests_parameters", func(tx *gorm.DB) {
		statement, parameters = tx.Statement.SQL.String(), len(tx.Statement.Vars)
	}); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]string{nil, {}, {"OpenAI"}, {"具身智能", `引号"`, `反\斜杠`}} {
		if err := NewRepository(orm).SaveInterests(context.Background(), 1, values, values); err != nil {
			t.Fatal(err)
		}
		if parameters != 3 || !strings.Contains(statement, "VALUES($1,$2,$3)") {
			t.Errorf("array length %d generated %d parameters: %s", len(values), parameters, statement)
		}
	}
}
