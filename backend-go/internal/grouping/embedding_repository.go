package grouping

import (
	"context"
	"errors"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

func (r *GormRepository) LoadEmbeddings(ctx context.Context, modelName string, articleIDs []string) (map[string]StoredEmbedding, error) {
	if err := validateRepository(r); err != nil {
		return nil, err
	}
	result := make(map[string]StoredEmbedding, len(articleIDs))
	if len(articleIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ID     string `gorm:"column:ref_id"`
		Hash   string `gorm:"column:text_hash"`
		Vector []byte `gorm:"column:vector"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT ref_id, text_hash, array_send(vector) AS vector
		FROM embeddings WHERE kind = 'article' AND model = ? AND ref_id IN ?`, modelName, articleIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		vector, ok := decodePostgresFloatArray(row.Vector)
		if !ok {
			continue
		}
		result[row.ID] = StoredEmbedding{ID: row.ID, Hash: row.Hash, Vector: vector}
	}
	return result, nil
}

func (r *GormRepository) SaveEmbeddingsAndComplete(ctx context.Context, modelName string, rows []StoredEmbedding, receiptID int64) error {
	if err := validateRepository(r); err != nil {
		return err
	}
	if modelName == "" || receiptID <= 0 {
		return errors.New("embedding persistence is incomplete")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if row.ID == "" || row.Hash == "" || !validVector(row.Vector, 0) {
				return errors.New("embedding row is invalid")
			}
			if err := tx.Exec(`INSERT INTO embeddings (kind, ref_id, model, text_hash, vector)
				VALUES ('article', ?, ?, ?, ?)
				ON CONFLICT (kind, ref_id, model) DO UPDATE SET text_hash = EXCLUDED.text_hash,
				vector = EXCLUDED.vector, created_at = now()`, row.ID, modelName, row.Hash, row.Vector).Error; err != nil {
				return err
			}
		}
		updated := tx.Model(&model.PaidReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{
			"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("embedding receipt was not found")
		}
		return nil
	})
}
