package tagging

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

/** nominate inserts a bounded candidate after checking all names under the lifecycle lock. */
func (r *Repository) nominate(ctx context.Context, l Label) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		labels, err := (&Repository{db: tx}).catalog(ctx)
		if err != nil {
			return err
		}
		active := 0
		for _, other := range labels {
			if other.Status == "candidate" {
				active++
			}
			if other.ID == l.ID {
				return nil
			}
			for _, term := range append([]string{other.Name}, other.Aliases...) {
				if nameKey(term) == l.NameKey {
					return nil
				}
			}
		}
		if active >= 100 {
			return nil
		}
		if err := tx.Table("interest_labels").Create(&l).Error; err != nil {
			return err
		}
		return audit(tx, "worker", l.ID, "nominate", "信源文章证据", "candidate")
	})
}

/** age records fresh evidence and expires labels without enough recent support. */
func (r *Repository) age(ctx context.Context, l Label, latest *time.Time, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		status := l.Status
		var row struct {
			Status         string
			CreatedAt      time.Time
			PromotedAt     *time.Time
			LastEvidenceAt *time.Time
		}
		if err := tx.Table("interest_labels").Where("id=?", l.ID).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != l.Status {
			return nil
		}
		last := row.CreatedAt
		for _, t := range []*time.Time{row.PromotedAt, row.LastEvidenceAt, latest} {
			if t != nil && t.After(last) {
				last = *t
			}
		}
		switch {
		case status == "candidate" && !last.After(now.AddDate(0, 0, -60)):
			status = "archived"
		case status == "active" && !last.After(now.AddDate(0, 0, -90)):
			status = "dormant"
		case status == "archived" && latest != nil && (row.LastEvidenceAt == nil || latest.After(*row.LastEvidenceAt)):
			var used int64
			if err := tx.Table("interest_labels").Where("status='candidate'").Count(&used).Error; err != nil {
				return err
			}
			if used < 100 {
				status = "candidate"
			}
		}
		if latest != nil {
			if err := tx.Exec(`UPDATE interest_labels SET last_evidence_at=greatest(last_evidence_at,?) WHERE id=?`, *latest, l.ID).Error; err != nil {
				return err
			}
		}
		if status == l.Status {
			return nil
		}
		if err := tx.Exec("UPDATE interest_labels SET status=? WHERE id=?", status, l.ID).Error; err != nil {
			return err
		}
		return audit(tx, "worker", l.ID, "age", "按文章证据更新生命周期", status)
	})
}

/** promote checks evidence and the weekly quota again in the publication transaction. */
func (r *Repository) promote(ctx context.Context, l Label, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		var current Label
		if err := tx.Table("interest_labels").Where("id=?", l.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status != "candidate" && current.Status != "dormant" {
			return nil
		}
		rows, err := (&Repository{db: tx}).evidenceArticles(ctx, l.ID, 30)
		if err != nil {
			return err
		}
		e := summarizeEvidence(rows)
		if e.Articles < 5 || e.Publishers < 3 || e.Weeks < 2 {
			return nil
		}
		if current.Status == "candidate" {
			var used int64
			if err := tx.Raw(`SELECT count(*) FROM audit_log WHERE action='label.promote'
				AND created_at >= date_trunc('week', ?::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'`, now).Scan(&used).Error; err != nil || used >= 3 {
				return err
			}
		}
		if err := tx.Exec("UPDATE interest_labels SET status='active',promoted_at=coalesce(promoted_at,?) WHERE id=?", now, l.ID).Error; err != nil {
			return err
		}
		action := "promote"
		if current.Status == "dormant" {
			action = "restore"
		}
		return audit(tx, "worker", l.ID, action, "证据门槛及语义复核通过", "active")
	})
}

/** curate applies a merge or withdrawal and keeps old IDs resolvable. */
func (r *Repository) curate(ctx context.Context, actor, id, action, target, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCatalog(tx); err != nil {
			return err
		}
		var from Label
		if err := tx.Table("interest_labels").Where("id=?", id).Take(&from).Error; err != nil || from.Status == "merged" || from.Status == "withdrawn" {
			return ErrInvalidChange
		}
		if action == "withdraw" {
			if err := tx.Exec("UPDATE interest_labels SET status='withdrawn' WHERE id=?", id).Error; err != nil {
				return err
			}
			return audit(tx, actor, id, action, reason, "withdrawn")
		}
		var into Label
		if err := tx.Table("interest_labels").Where("id=?", target).Take(&into).Error; err != nil {
			return ErrInvalidChange
		}
		mergeCandidates := actor == "worker" && from.Status == "candidate" && into.Status == "candidate"
		if id == target || from.Kind != into.Kind || (!mergeCandidates && into.Status != "active" && into.Status != "dormant") {
			return ErrInvalidChange
		}
		for _, term := range append([]string{from.Name}, from.Aliases...) {
			if !strings.EqualFold(term, into.Name) {
				into.Aliases = append(into.Aliases, term)
			}
		}
		if err := tx.Exec(`UPDATE interest_labels SET aliases=ARRAY(SELECT DISTINCT term FROM unnest(?::text[]) term ORDER BY term)
			WHERE id=?`, into.Aliases, target).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE interest_labels SET merged_into=? WHERE merged_into=?", target, id).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE interest_labels SET status='merged',merged_into=? WHERE id=?", target, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO article_interest_labels(article_id,label_id)
			SELECT article_id,? FROM article_interest_labels WHERE label_id=? ON CONFLICT DO NOTHING`, target, id).Error; err != nil {
			return err
		}
		return audit(tx, actor, id, action, reason, target)
	})
}

/** audit records both automatic and administrative label decisions. */
func audit(tx *gorm.DB, actor, id, action, reason, state string) error {
	return tx.Exec(`INSERT INTO audit_log(actor,action,subject,reason,"after")
		VALUES(?,?,?,?,jsonb_build_object('state',?::text))`, actor, "label."+action, "label:"+id, reason, state).Error
}
