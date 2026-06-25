package optimize

import (
	"math"
	"sync"
)

//nolint:gochecknoglobals // global read-only lookup tables for impasto and canvas simulation performance
var (
	lutWeave          [400]float64
	lutVarnishPool    [400]float64
	lutWeaveOnce      sync.Once
	lutCraquelure     [65536]float64
	lutCraquelureOnce sync.Once
)

func initializeCraquelure() {
	type pt struct {
		x, y float64
	}
	seeds := make([]pt, 16)
	state := uint32(12345)
	nextRand := func() float64 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return float64(state) / float64(0xFFFFFFFF)
	}

	for i := 0; i < 16; i++ {
		seeds[i] = pt{x: nextRand() * 256.0, y: nextRand() * 256.0}
	}

	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			f1 := 999999.0
			f2 := 999999.0

			for _, s := range seeds {
				dx := math.Abs(float64(x) - s.x)
				if dx > 128.0 {
					dx = 256.0 - dx
				}
				dy := math.Abs(float64(y) - s.y)
				if dy > 128.0 {
					dy = 256.0 - dy
				}
				d := math.Sqrt(dx*dx + dy*dy)

				if d < f1 {
					f2 = f1
					f1 = d
				} else if d < f2 {
					f2 = d
				}
			}

			diff := f2 - f1
			var val float64
			if diff < 2.0 {
				val = -0.4 * (1.0 - diff/2.0)
			}
			lutCraquelure[y*256+x] = val
		}
	}
}

func lookupCraquelure(x, y int) float64 {
	return lutCraquelure[(y%256)*256+(x%256)]
}

// calculateWarpCell calculates weave topography for a warp cell.
func calculateWarpCell(cellX int, lightDirX float64) float64 {
	nx := (float64(cellX) - 4.5) / 5.0
	diffuse := nx * lightDirX
	if diffuse < 0 {
		diffuse = 0
	}
	return 0.4 + (diffuse * 0.3)
}

// calculateWeftCell calculates weave topography for a weft cell.
func calculateWeftCell(cellY int, lightDirY float64) float64 {
	ny := (float64(cellY) - 4.5) / 5.0
	diffuse := ny * lightDirY
	if diffuse < 0 {
		diffuse = 0
	}
	weave := 0.4 + (diffuse * 0.3)

	absNy := ny
	if absNy < 0 {
		absNy = -absNy
	}
	if absNy < 0.2 {
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
