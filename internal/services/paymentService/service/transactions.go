package service

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
)

func (s *service) ListTransactions(ctx context.Context, userID primitive.ObjectID) ([]dto.TransactionDTO, error) {
	txns, err := s.store.ListTransactionsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := make([]dto.TransactionDTO, 0, len(txns))
	for _, t := range txns {
		out = append(out, dto.TransactionDTO{
			PaddleTransactionID: t.PaddleTransactionID,
			Status:              t.Status,
			AmountTotal:         t.AmountTotal,
			CurrencyCode:        t.CurrencyCode,
			BilledAt:            t.BilledAt,
			InvoiceNumber:       t.InvoiceNumber,
			CreatedAt:           t.CreatedAt,
		})
	}
	return out, nil
}
