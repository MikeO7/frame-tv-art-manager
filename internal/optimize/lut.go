package optimize

import (
	"math"
	"sync"
)

//nolint:gochecknoglobals // global read-only lookup tables for impasto and canvas simulation performance
var (
	lutWeave       [400]float64
	lutVarnishPool [400]float64
	lutWeaveOnce   sync.Once
)

// craquelureCell is the spacing of the crack network's seed grid in pixels.
const craquelureCell = 64

// craquelureReach is how many cells away a pixel's two nearest seeds can lie.
// A seed in the pixel's own cell is within one cell diagonal, and every seed
// three cells away is farther than that.
const craquelureReach = 2

// craquelureField is the crack network of an aged varnish for one image: one
// seed per grid cell, each placed by a hash of its cell, with a hairline crack
// along the borders between neighboring seeds' regions. Because the seeds
// come from a hash and not a tile, the network does not repeat across the
// frame.
type craquelureField struct {
	columns, rows int
	seedX, seedY  []float64
}

func newCraquelureField(width, height int) craquelureField {
	field := craquelureField{
		columns: (width+craquelureCell-1)/craquelureCell + 2*craquelureReach,
		rows:    (height+craquelureCell-1)/craquelureCell + 2*craquelureReach,
	}
	field.seedX = make([]float64, field.columns*field.rows)
	field.seedY = make([]float64, field.columns*field.rows)
	for row := range field.rows {
		for column := range field.columns {
			cellX, cellY := column-craquelureReach, row-craquelureReach
			offsetX, offsetY := craquelureJitter(cellX, cellY)
			field.seedX[row*field.columns+column] = (float64(cellX) + offsetX) * craquelureCell
			field.seedY[row*field.columns+column] = (float64(cellY) + offsetY) * craquelureCell
		}
	}
	return field
}

// craquelureJitter returns a cell's seed position within the cell, in 0..1
// on each axis, from an integer hash of the cell coordinates.
func craquelureJitter(cellX, cellY int) (float64, float64) {
	//nolint:gosec // wrapping conversion is the point of a hash
	hash := uint32(cellX)*0x9E3779B1 ^ uint32(cellY)*0x85EBCA77
	hash ^= hash >> 15
	hash *= 0x2C1B3C6D
	hash ^= hash >> 12
	hash *= 0x297A2D39
	hash ^= hash >> 15
	return float64(hash&0xFFFF) / 65536, float64(hash>>16) / 65536
}

// at returns the darkening a crack contributes at a pixel: zero away from
// region borders, down to -0.4 on one.
func (field *craquelureField) at(x, y int) float64 {
	const crackWidth = 2.0
	pointX, pointY := float64(x), float64(y)
	nearest, second := math.MaxFloat64, math.MaxFloat64
	column, row := x/craquelureCell, y/craquelureCell
	for cellRow := row; cellRow <= row+2*craquelureReach; cellRow++ {
		base := cellRow * field.columns
		for cell := base + column; cell <= base+column+2*craquelureReach; cell++ {
			dx, dy := pointX-field.seedX[cell], pointY-field.seedY[cell]
			squared := dx*dx + dy*dy
			if squared < nearest {
				nearest, second = squared, nearest
			} else if squared < second {
				second = squared
			}
		}
	}
	gap := math.Sqrt(second) - math.Sqrt(nearest)
	if gap >= crackWidth {
		return 0
	}
	return -0.4 * (1 - gap/crackWidth)
}

// calculateWarpCell calculates weave topography for a warp cell.
func calculateWarpCell(cellX int, lightDirX float64) float64 {
	nx := (float64(cellX) - 4.5) / 5.0
	return 0.4 + (max(nx*lightDirX, 0) * 0.3)
}

// calculateWeftCell calculates weave topography for a weft cell.
func calculateWeftCell(cellY int, lightDirY float64) float64 {
	ny := (float64(cellY) - 4.5) / 5.0
	weave := 0.4 + (max(ny*lightDirY, 0) * 0.3)
	if math.Abs(ny) < 0.2 {
		weave += 0.15
	}
	return weave
}

func calculateWeave(x, y int) (float64, float64) {
	lutWeaveOnce.Do(func() {
		for wy := 0; wy < 20; wy++ {
			for wx := 0; wx < 20; wx++ {
				idX, idY := wx/10, wy/10
				cellX, cellY := wx%10, wy%10
				isWarp := (idX+idY)%2 == 0

				var weave float64
				lightDirX, lightDirY := -0.707, -0.707

				if isWarp {
					weave = calculateWarpCell(cellX, lightDirX)
				} else {
					weave = calculateWeftCell(cellY, lightDirY)
				}

				isValley := cellX == 0 || cellX == 9 || cellY == 0 || cellY == 9
				varnishPool := 1.0
				if isValley {
					weave *= 0.8
					varnishPool = 0.96
				}

				idx := wy*20 + wx
				lutWeave[idx] = weave
				lutVarnishPool[idx] = varnishPool
			}
		}
	})

	idx := (y%20)*20 + (x % 20)
	return lutWeave[idx], lutVarnishPool[idx]
}
