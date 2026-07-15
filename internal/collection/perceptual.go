package collection

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math/bits"
	"sort"

	"golang.org/x/image/draw"
)

type visualFingerprint struct {
	name string
	hash uint64
}

type visualHashNode struct {
	fingerprint visualFingerprint
	duplicates  []visualFingerprint
	children    map[int]*visualHashNode
}

const maxPerceptualAdvisories = 100

func (s *store) perceptualAdvisories(ctx context.Context, items []Item) ([]string, error) {
	if !s.perceptualDuplicates || len(items) < 2 {
		return nil, nil
	}
	fingerprints := make([]visualFingerprint, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !item.visualHashValid {
			continue
		}
		fingerprints = append(fingerprints, visualFingerprint{name: item.Name, hash: item.visualHash})
	}
	advisories := make([]string, 0)
	var root *visualHashNode
	omitted := 0
	for _, fingerprint := range fingerprints {
		remaining := max(maxPerceptualAdvisories-len(advisories), 0)
		matches, totalMatches := root.search(fingerprint.hash, s.perceptualDuplicateDistance, remaining, nil)
		for _, match := range matches {
			distance := bits.OnesCount64(match.hash ^ fingerprint.hash)
			advisories = append(advisories, fmt.Sprintf("probable visual duplicate: %s and %s (distance %d)", match.name, fingerprint.name, distance))
		}
		omitted += totalMatches - len(matches)
		root = root.insert(fingerprint)
	}
	sort.Strings(advisories)
	if omitted > 0 {
		advisories = append(advisories, fmt.Sprintf("%d additional probable visual-duplicate pairs omitted", omitted))
	}
	return advisories, nil
}

func (node *visualHashNode) insert(fingerprint visualFingerprint) *visualHashNode {
	if node == nil {
		return &visualHashNode{fingerprint: fingerprint, children: make(map[int]*visualHashNode)}
	}
	distance := bits.OnesCount64(node.fingerprint.hash ^ fingerprint.hash)
	if distance == 0 {
		node.duplicates = append(node.duplicates, fingerprint)
		return node
	}
	child := node.children[distance]
	node.children[distance] = child.insert(fingerprint)
	return node
}

func (node *visualHashNode) search(hash uint64, threshold, limit int, matches []visualFingerprint) ([]visualFingerprint, int) {
	if node == nil {
		return matches, 0
	}
	distance := bits.OnesCount64(node.fingerprint.hash ^ hash)
	total := 0
	if distance <= threshold {
		total = 1 + len(node.duplicates)
		available := limit - len(matches)
		if available > 0 {
			matches = append(matches, node.fingerprint)
			available--
			matches = append(matches, node.duplicates[:min(available, len(node.duplicates))]...)
		}
	}
	edges := make([]int, 0, len(node.children))
	for edge := range node.children {
		edges = append(edges, edge)
	}
	sort.Ints(edges)
	for _, edge := range edges {
		child := node.children[edge]
		if edge >= distance-threshold && edge <= distance+threshold {
			var childTotal int
			matches, childTotal = child.search(hash, threshold, limit, matches)
			total += childTotal
		}
	}
	return matches, total
}

func differenceHash(src image.Image) uint64 {
	gray := image.NewGray(image.Rect(0, 0, 9, 8))
	draw.CatmullRom.Scale(gray, gray.Bounds(), src, src.Bounds(), draw.Src, nil)
	var hash uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			left := color.GrayModel.Convert(gray.At(x, y)).(color.Gray).Y
			right := color.GrayModel.Convert(gray.At(x+1, y)).(color.Gray).Y
			hash <<= 1
			if left > right {
				hash |= 1
			}
		}
	}
	return hash
}
