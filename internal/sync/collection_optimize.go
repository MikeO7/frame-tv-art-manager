package sync

import (
	"context"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func (c *localCollection) optimize(ctx context.Context, request optimize.StageRequest) (*optimize.Stage, error) {
	var stage *optimize.Stage
	run := func(runCtx context.Context) error {
		var err error
		stage, err = optimize.StageCatalog(runCtx, request)
		return err
	}
	err := c.limits.run(ctx, false, run)
	return stage, err
}

func stageInputs(root string, snapshot collectionpkg.Snapshot) []optimize.StageInput {
	inputs := make([]optimize.StageInput, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		originals := make([]optimize.StageOriginal, 0, len(item.Originals))
		for _, original := range item.Originals {
			originals = append(originals, optimize.StageOriginal{
				Digest: original.Digest, Path: collectionpkg.OriginalPath(root, original),
			})
		}
		inputs = append(inputs, optimize.StageInput{
			Name: item.Name, Key: item.Key, Path: item.Path, Digest: item.Digest,
			Width: item.Width, Height: item.Height,
			SourceKeys: append([]string(nil), item.SourceKeys...), TransformKey: item.TransformKey,
			Derivative: optimize.DerivativeKind(item.Derivative), Originals: originals,
		})
	}
	return inputs
}
