package optimize

import "image"

func calculateIntegralImage(saliencyMap []float64, w, h int) []float64 {
	integral := make([]float64, w*h)
	for y := 0; y < h; y++ {
		rowSum := 0.0
		yW := y * w
		prevYW := (y - 1) * w
		for x := 0; x < w; x++ {
			rowSum += saliencyMap[yW+x]
			if y == 0 {
				integral[yW+x] = rowSum
			} else {
				integral[yW+x] = integral[prevYW+x] + rowSum
			}
		}
	}
	return integral
}

// getRectSum returns the saliency sum over the rectangle r using the
// summed-area table. NOTE: r.Max is treated as inclusive (matching
// integral-image indexing), not the usual exclusive image.Rectangle bound.
func getRectSum(integral []float64, r image.Rectangle, w int) float64 {
	x1, y1, x2, y2 := r.Min.X, r.Min.Y, r.Max.X, r.Max.Y
	res := integral[y2*w+x2]
	if x1 > 0 {
		res -= integral[y2*w+x1-1]
	}
	if y1 > 0 {
		res -= integral[(y1-1)*w+x2]
	}
	if x1 > 0 && y1 > 0 {
		res += integral[(y1-1)*w+x1-1]
	}
	return res
}
