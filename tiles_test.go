package earcut

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

type tilePolygon struct {
	outer []Point[float64]
	holes [][]Point[float64]
}

// readTilesFixture reads testdata/tiles-fixture.bin, copied from upstream's bench directory.
// It holds Mapbox Vector Tile (MVT) polygon geometries, stored as LEB128 varints:
//
//	file    := tile*
//	tile    := zoom featureCount feature*
//	feature := geomLen geom   (geomLen = number of varints in geom)
//	geom    := uint32*        (raw MVT command and parameter integers)
//
// Multipolygons are split into polygons, and rings are classified as outer ring or hole
// by the sign of their area, as the MVT spec defines.
func readTilesFixture(t testing.TB) []tilePolygon {
	buf, err := os.ReadFile("testdata/tiles-fixture.bin")
	require.NoError(t, err)

	r := &varintReader{buf: buf}

	var polygons []tilePolygon

	for r.pos < len(buf) {
		_ = r.next() // zoom
		features := r.next()

		for range features {
			geom := make([]uint32, r.next())
			for i := range geom {
				geom[i] = r.next()
			}

			var current *tilePolygon

			for _, ring := range decodeMvtRings(geom) {
				if len(ring) < 3 {
					continue
				}

				area := ringArea(ring)
				switch {
				case area > 0:
					if current != nil {
						polygons = append(polygons, *current)
					}

					current = &tilePolygon{outer: ring}

				case area < 0 && current != nil:
					current.holes = append(current.holes, ring)
				}
			}

			if current != nil {
				polygons = append(polygons, *current)
			}
		}
	}

	return polygons
}

type varintReader struct {
	buf []byte
	pos int
}

func (r *varintReader) next() uint32 {
	var value uint32
	var shift uint

	for {
		b := r.buf[r.pos]
		r.pos++

		value |= uint32(b&0x7f) << shift
		shift += 7

		if b&0x80 == 0 {
			return value
		}
	}
}

func decodeMvtRings(geom []uint32) [][]Point[float64] {
	var rings [][]Point[float64]
	var ring []Point[float64]
	var x, y int64

	for i := 0; i < len(geom); {
		cmd := geom[i] & 0x7
		count := int(geom[i] >> 3)
		i++

		switch {
		case cmd == 1: // MoveTo
			for range count {
				x += zigZagDecode(geom[i])
				y += zigZagDecode(geom[i+1])
				i += 2

				if ring != nil {
					rings = append(rings, ring)
				}

				ring = []Point[float64]{{X: float64(x), Y: float64(y)}}
			}

		case cmd == 2: // LineTo
			for range count {
				x += zigZagDecode(geom[i])
				y += zigZagDecode(geom[i+1])
				i += 2

				ring = append(ring, Point[float64]{X: float64(x), Y: float64(y)})
			}

		case cmd == 7 && ring != nil: // ClosePath
			rings = append(rings, ring)
			ring = nil
		}
	}

	if ring != nil {
		rings = append(rings, ring)
	}

	return rings
}

func zigZagDecode(n uint32) int64 {
	// upstream uses a signed shift, so do the same
	v := int32(n)
	return int64((v >> 1) ^ -(v & 1))
}

func ringArea(ring []Point[float64]) float64 {
	var sum float64
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		sum += (ring[j].X - ring[i].X) * (ring[i].Y + ring[j].Y)
	}

	return sum / 2
}
