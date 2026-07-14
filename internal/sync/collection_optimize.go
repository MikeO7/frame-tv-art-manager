package sync

import (
	"context"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/resources"
)

func (c *localCollection) optimize(ctx context.Context, request optimize.StageRequest) (*optimize.Stage, error) {
	var stage *optimize.Stage
	run := func(runCtx context.Context) error {
		var err error
		stage, err = optimize.StageCatalog(runCtx, request)
		return err
	}
	if c.limits == nil {
		err := run(ctx)
		return stage, err
	}
	err := c.limits.Run(ctx, resources.Request{Class: resources.Background, Mode: "collection-prepare"}, run)
	return stage, err
}

func stageInputs(snapshot collectionpkg.Snapshot) []optimize.StageInput {
	inputs := make([]optimize.StageInput, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		inputs = append(inputs, optimize.StageInput{
			Name: item.Name, Path: item.Path, Digest: item.Digest, Width: item.Width, Height: item.Height,
		})
	}
	return inputs
}
