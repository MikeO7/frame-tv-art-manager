package collection

import "fmt"

func encodeVisualHash(item Item) string {
	if !item.visualHashValid {
		return ""
	}
	return fmt.Sprintf("%016x", item.visualHash)
}
