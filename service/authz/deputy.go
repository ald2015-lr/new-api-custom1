package authz

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A deputy admin is a role-10 user with one casbin_rule row
// ('g', 'user:<id>', 'role:deputy'). The adapter does not load g rows into the
// policy snapshot; the functions below read the assignment straight from the
// database, so an assignment applies on every node at once.
//
// IsDeputy, DeputyUserIDs and AllDeputyUserIDs (and Can, which calls IsDeputy)
// use the database Init received, through its own connection. Never call them
// while a transaction is open on that database: on a single-connection SQLite
// database that deadlocks, and elsewhere they would not see uncommitted rows.
// Inside a transaction use deputyAssigned(tx, ...).

func currentPolicyDB() (*gorm.DB, error) {
	enforcerMu.RLock()
	defer enforcerMu.RUnlock()
	if policyDB == nil {
		return nil, errors.New("authz policy database is not initialized")
	}
	return policyDB, nil
}

func deputyAssignments(db *gorm.DB) *gorm.DB {
	return db.Model(&model.CasbinRule{}).Where("ptype = ? AND v1 = ?", "g", RoleSubject(BuiltInRoleDeputy))
}

func deputyAssigned(db *gorm.DB, userID int) (bool, error) {
	var count int64
	if err := deputyAssignments(db).Where("v0 = ?", UserSubject(userID)).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsDeputy reports whether userID carries the deputy admin assignment. It reads
// the database, not the policy snapshot, so an assignment applies on every node
// at once.
func IsDeputy(userID int) (bool, error) {
	db, err := currentPolicyDB()
	if err != nil {
		return false, err
	}
	return deputyAssigned(db, userID)
}

// DeputyUserIDs returns which of ids carry the deputy assignment (one IN query).
func DeputyUserIDs(ids []int) (map[int]bool, error) {
	deputies := make(map[int]bool, len(ids))
	if len(ids) == 0 {
		return deputies, nil
	}
	db, err := currentPolicyDB()
	if err != nil {
		return nil, err
	}
	subjects := make(map[string]int, len(ids))
	for _, id := range ids {
		subjects[UserSubject(id)] = id
	}
	var assigned []string
	if err := deputyAssignments(db).Where("v0 IN ?", slices.Collect(maps.Keys(subjects))).Pluck("v0", &assigned).Error; err != nil {
		return nil, err
	}
	for _, subject := range assigned {
		deputies[subjects[subject]] = true
	}
	return deputies, nil
}

// AllDeputyUserIDs lists every user carrying the deputy assignment, in
// ascending order; rows whose subject is not "user:<int>" are skipped.
func AllDeputyUserIDs() ([]int, error) {
	db, err := currentPolicyDB()
	if err != nil {
		return nil, err
	}
	var assigned []string
	if err := deputyAssignments(db).Pluck("v0", &assigned).Error; err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(assigned))
	for _, subject := range assigned {
		rest, ok := strings.CutPrefix(subject, "user:")
		if !ok {
			continue
		}
		id, err := strconv.Atoi(rest)
		if err != nil || id <= 0 || UserSubject(id) != subject {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// AssignDeputyInTx marks userID as a deputy admin (idempotent, ON CONFLICT DO
// NOTHING) and deletes the user's p rows on recharge resources, which can never
// apply to a deputy.
func AssignDeputyInTx(tx *gorm.DB, userID int) error {
	assigned, err := deputyAssigned(tx, userID)
	if err != nil {
		return err
	}
	if !assigned {
		rule := newRule("g", []string{UserSubject(userID), RoleSubject(BuiltInRoleDeputy)})
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rule).Error; err != nil {
			return err
		}
	}
	for _, resource := range registry {
		if !resource.Recharge {
			continue
		}
		if err := tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(userID), resource.Resource).Delete(&model.CasbinRule{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// UnassignDeputyInTx removes the deputy assignment of userID.
func UnassignDeputyInTx(tx *gorm.DB, userID int) error {
	return tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "g", UserSubject(userID), RoleSubject(BuiltInRoleDeputy)).Delete(&model.CasbinRule{}).Error
}
