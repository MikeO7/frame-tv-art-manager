sed -i '689,715c\
					// Faster clamp and cast without math.Max/Min\
					switch {\
					case r < 0:\
						src.Pix[i] = 0\
					case r > 255:\
						src.Pix[i] = 255\
					default:\
						src.Pix[i] = uint8(r)\
					}\
\
					switch {\
					case g < 0:\
						src.Pix[i+1] = 0\
					case g > 255:\
						src.Pix[i+1] = 255\
					default:\
						src.Pix[i+1] = uint8(g)\
					}\
\
					switch {\
					case b < 0:\
						src.Pix[i+2] = 0\
					case b > 255:\
						src.Pix[i+2] = 255\
					default:\
						src.Pix[i+2] = uint8(b)\
					}' internal/optimize/resize.go
