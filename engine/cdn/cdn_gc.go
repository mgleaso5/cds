package cdn

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ovh/cds/engine/cdn/index"
	"github.com/ovh/cds/engine/cdn/storage"
	"github.com/ovh/cds/engine/cdn/storage/cds"
	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/log"
	"github.com/ovh/cds/sdk/telemetry"
)

const (
	ItemLogGC = 24 * 3600
)

func (s *Service) itemPurge(ctx context.Context) {
	tickPurge := time.NewTicker(1 * time.Minute)
	defer tickPurge.Stop()
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() != nil {
				log.Error(ctx, "cdn:ItemPurge: %v", ctx.Err())
			}
			return
		case <-tickPurge.C:
			if err := s.cleanItemToDelete(ctx); err != nil {
				log.ErrorWithFields(ctx, logrus.Fields{"stack_trace": fmt.Sprintf("%+v", err)}, "%s", err)
			}
		}
	}
}

// ItemsGC clean long incoming item + delete item from buffer when synchronized everywhere
func (s *Service) itemsGC(ctx context.Context) {
	tickGC := time.NewTicker(1 * time.Minute)
	defer tickGC.Stop()
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() != nil {
				log.Error(ctx, "cdn:CompleteWaitingItems: %v", ctx.Err())
			}
			return
		case <-tickGC.C:
			if err := s.cleanBuffer(ctx); err != nil {
				log.ErrorWithFields(ctx, logrus.Fields{"stack_trace": fmt.Sprintf("%+v", err)}, "%s", err)
			}
			if err := s.cleanWaitingItem(ctx); err != nil {
				log.ErrorWithFields(ctx, logrus.Fields{"stack_trace": fmt.Sprintf("%+v", err)}, "%s", err)
			}
		}
	}
}

func (s *Service) cleanItemToDelete(ctx context.Context) error {
	for {
		ids, err := index.LoadItemIDsToDelete(s.mustDBWithCtx(ctx), 100)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		if err := index.DeleteItemByIDs(s.mustDBWithCtx(ctx), ids); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cleanBuffer(ctx context.Context) error {
	var cdsBackendID string
	for _, sto := range s.Units.Storages {
		_, ok := sto.(*cds.CDS)
		if !ok {
			continue
		}
		cdsBackendID = sto.ID()
		break
	}
	if cdsBackendID == "" {
		return nil
	}
	itemIDs, err := storage.LoadAllItemsIDInBufferAndAllUnitsExceptCDS(s.mustDBWithCtx(ctx), cdsBackendID)
	if err != nil {
		return err
	}
	tx, err := s.mustDBWithCtx(ctx).Begin()
	if err != nil {
		return sdk.WrapError(err, "unable to start transaction")
	}
	defer tx.Rollback() //nolint
	if err := storage.DeleteItemsUnit(tx, s.Units.Buffer.ID(), itemIDs); err != nil {
		return err
	}
	return sdk.WithStack(tx.Commit())
}

func (s *Service) cleanWaitingItem(ctx context.Context) error {
	itemUnits, err := storage.LoadOldItemUnitByItemStatusAndDuration(ctx, s.Mapper, s.mustDBWithCtx(ctx), index.StatusItemIncoming, ItemLogGC)
	if err != nil {
		return err
	}
	log.Debug("cdn:CompleteWaitingItems: %d items to complete", len(itemUnits))
	for _, itemUnit := range itemUnits {
		tx, err := s.mustDBWithCtx(ctx).Begin()
		if err != nil {
			return sdk.WrapError(err, "unable to start transaction")
		}
		if err := s.completeItem(ctx, tx, itemUnit); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return err
		}
		telemetry.Record(ctx, metricsItemCompletedByGC, 1)
	}
	return nil
}