package samsung

import (
	"context"
	"time"
)

type storageCapacityTransport interface {
	StorageCapacity(context.Context) (int64, error)
}

func (a *adapter) observeStorage(ctx context.Context, observedAt time.Time) (StorageObservation, error) {
	transport, supported := a.transport.(storageCapacityTransport)
	if !supported {
		return StorageObservation{}, nil
	}
	totalBytes, err := transport.StorageCapacity(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return StorageObservation{}, err
		}
		a.logger.Warn("TV storage capacity unavailable", "error", err)
		return StorageObservation{}, nil
	}
	if totalBytes <= 0 {
		return StorageObservation{}, nil
	}
	return StorageObservation{TotalBytes: totalBytes, Known: true, ObservedAt: observedAt}, nil
}
