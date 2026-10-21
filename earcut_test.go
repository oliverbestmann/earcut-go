package earcut

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expected results, copied from upstream's test/expected.json
type expectations struct {
	Triangles          map[string]int     `json:"triangles"`
	Errors             map[string]float64 `json:"errors"`
	ErrorsWithRotation map[string]float64 `json:"errors-with-rotation"`
}

func loadExpectations(t testing.TB) expectations {
	buf, err := os.ReadFile("testdata/expected.json")
	require.NoError(t, err)

	var expected expectations
	require.NoError(t, json.Unmarshal(buf, &expected))
	return expected
}

func loadFixture(t testing.TB, name string) [][][2]float64 {
	buf, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err)

	var rings [][][2]float64
	require.NoError(t, json.Unmarshal(buf, &rings))
	return rings
}

// toPolygon splits rings into the outer ring and the holes
func toPolygon(rings [][][2]float64) ([]Point[float64], [][]Point[float64]) {
	var converted [][]Point[float64]
	for _, ring := range rings {
		points := make([]Point[float64], len(ring))
		for i, p := range ring {
			points[i] = Point[float64]{X: p[0], Y: p[1]}
		}

		converted = append(converted, points)
	}

	return converted[0], converted[1:]
}

// rotate rotates all coordinates by a multiple of 90 degrees
func rotate(rings [][][2]float64, rotation int) [][][2]float64 {
	theta := float64(rotation) * math.Pi / 180
	xx, xy := math.Round(math.Cos(theta)), math.Round(-math.Sin(theta))
	yx, yy := math.Round(math.Sin(theta)), math.Round(math.Cos(theta))

	var rotated [][][2]float64
	for _, ring := range rings {
		var r [][2]float64
		for _, p := range ring {
			r = append(r, [2]float64{xx*p[0] + xy*p[1], yx*p[0] + yy*p[1]})
		}

		rotated = append(rotated, r)
	}

	return rotated
}

func BenchmarkEarCut(b *testing.B) {
	outer, holes := toPolygon(loadFixture(b, "water-huge"))

	b.ReportAllocs()

	for b.Loop() {
		_, _ = Triangulate(outer, holes)
	}
}

func BenchmarkTiles(b *testing.B) {
	polygons := readTilesFixture(b)

	b.ReportAllocs()

	for b.Loop() {
		for _, p := range polygons {
			_, _ = Triangulate(p.outer, p.holes)
		}
	}
}

// BenchmarkTilesRefine measures Refine alone, like upstream's bench-refine.js:
// earcut output is computed once and restored untimed before each pass.
func BenchmarkTilesRefine(b *testing.B) {
	polygons := readTilesFixture(b)

	points := make([][]Point[float64], len(polygons))
	base := make([][]uint32, len(polygons))
	work := make([][]uint32, len(polygons))

	for i, p := range polygons {
		points[i], base[i] = Triangulate(p.outer, p.holes)
		work[i] = make([]uint32, len(base[i]))
	}

	b.ReportAllocs()

	for b.Loop() {
		b.StopTimer()
		for i := range base {
			copy(work[i], base[i])
		}
		b.StartTimer()

		for i := range work {
			Refine(work[i], points[i])
		}
	}
}

func TestFixtures(t *testing.T) {
	expected := loadExpectations(t)

	for name, expectedTriangles := range expected.Triangles {
		for _, rotation := range []int{0, 90, 180, 270} {
			t.Run(fmt.Sprintf("%s/rotation-%d", name, rotation), func(t *testing.T) {
				outer, holes := toPolygon(rotate(loadFixture(t, name), rotation))

				_, indices := Triangulate(outer, holes)

				if rotation == 0 {
					require.Equal(t, expectedTriangles, len(indices)/3)
				}

				expectedDeviation := expected.Errors[name]
				if rotation != 0 && expected.ErrorsWithRotation[name] != 0 {
					expectedDeviation = expected.ErrorsWithRotation[name]
				}

				if expectedTriangles > 0 {
					require.LessOrEqual(t, Deviation(outer, holes, indices), expectedDeviation)
				}
			})
		}
	}
}

// Triangulate and Refine reuse pooled buffers, concurrent calls must not share them.
func TestConcurrent(t *testing.T) {
	names := []string{"water-huge", "building", "hole-touching-outer", "steiner", "dude"}

	want := map[string][]uint32{}
	for _, name := range names {
		points, indices := Triangulate(toPolygon(loadFixture(t, name)))
		Refine(indices, points)
		want[name] = indices
	}

	square32 := []Point[float32]{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	_, wantSquare := Triangulate(square32, nil)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for range 10 {
				for _, name := range names {
					points, indices := Triangulate(toPolygon(loadFixture(t, name)))
					Refine(indices, points)
					assert.Equal(t, want[name], indices, name)

					// float32 calls get their own pooled state
					_, square := Triangulate(square32, nil)
					assert.Equal(t, wantSquare, square)
				}
			}
		}()
	}

	wg.Wait()
}

func TestIndices(t *testing.T) {
	_, indices := Triangulate([]Point[float64]{{10, 0}, {0, 50}, {60, 60}, {70, 10}}, nil)
	require.Equal(t, []uint32{1, 0, 3, 1, 3, 2}, indices)
}

func TestEmpty(t *testing.T) {
	_, indices := Triangulate[float64](nil, nil)
	require.Empty(t, indices)
}

func TestEmptyHoleIsIgnored(t *testing.T) {
	square := []Point[float64]{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	hole := []Point[float64]{{2, 2}, {4, 2}, {3, 4}}

	_, expected := Triangulate(square, nil)
	_, indices := Triangulate(square, [][]Point[float64]{{}})
	require.Equal(t, expected, indices)

	_, expected = Triangulate(square, [][]Point[float64]{hole})
	_, indices = Triangulate(square, [][]Point[float64]{hole, {}})
	require.Equal(t, expected, indices)
}

func TestInfiniteLoop(t *testing.T) {
	outer := []Point[float64]{{1, 2}, {2, 2}, {1, 2}, {1, 1}, {1, 2}}
	hole := []Point[float64]{{4, 1}, {5, 1}, {3, 2}, {4, 2}, {4, 1}}
	_, _ = Triangulate(outer, [][]Point[float64]{hole})
}

func TestLargeConvexPolygon(t *testing.T) {
	// a strictly convex ring skips the point checks of ear slicing. With them, each hashed check
	// of its fan of long triangles scans a large part of the ring, which takes seconds.
	const n = 100000

	polygon := make([]Point[float64], n)
	for i := range polygon {
		angle := 2 * math.Pi * float64(i) / n
		polygon[i] = Point[float64]{X: math.Cos(angle), Y: math.Sin(angle)}
	}

	_, indices := Triangulate(polygon, nil)
	require.Equal(t, n-2, len(indices)/3)
	require.Less(t, Deviation(polygon, nil, indices), 1e-12)
}

func TestNestedSplits(t *testing.T) {
	// a rectangle with k small triangular holes, each one step right and down from the
	// previous; every hole adds a level of nested splits
	const k = 600

	outer := []Point[float64]{{-1, -k}, {2*k + 1, -k}, {2*k + 1, 7}, {-1, 7}}

	var holes [][]Point[float64]
	for i := range k {
		x, y := float64(i), float64(i)
		holes = append(holes, []Point[float64]{{1 + 2*x, -y}, {2 * x, 6 - y}, {2 + 2*x, 6 - y}})
	}

	_, indices := Triangulate(outer, holes)
	require.Equal(t, 3002, len(indices)/3)
	require.Zero(t, Deviation(outer, holes, indices))
}

func TestCollinearPolygonHasZeroDeviation(t *testing.T) {
	// all points are on the line y = 2x
	polygon := []Point[float64]{{0.1, 0.2}, {1.3, 2.6}, {2.5, 5.0}, {3.7, 7.4}}

	_, indices := Triangulate(polygon, nil)
	require.Empty(t, indices)
	require.Zero(t, Deviation(polygon, nil, indices))
}

func TestRefineImprovesBadQuadDiagonal(t *testing.T) {
	points := []Point[float64]{{0, 0}, {3, 0}, {10, 1}, {0, 2}}
	indices := []uint32{2, 3, 0, 2, 0, 1}

	before := trianglePerimeter(indices, points)
	Refine(indices, points)
	after := trianglePerimeter(indices, points)

	require.Equal(t, []uint32{2, 3, 1, 3, 0, 1}, indices)
	require.Less(t, after, before*0.7)
	require.Zero(t, Deviation(points, nil, indices))
}

func TestRefineClockwiseQuadKeepsWinding(t *testing.T) {
	points := []Point[float64]{{0, 0}, {3, 0}, {10, 1}, {0, 2}}
	indices := []uint32{2, 0, 3, 2, 1, 0}

	Refine(indices, points)

	require.Equal(t, []uint32{1, 0, 3, 2, 1, 3}, indices)
	require.Zero(t, Deviation(points, nil, indices))
}

func TestRefineKeepsGoodQuadDiagonal(t *testing.T) {
	points := []Point[float64]{{0, 0}, {5, 0}, {4, 1}, {0, 4}}
	indices := []uint32{2, 3, 0, 2, 0, 1}

	Refine(indices, points)

	require.Equal(t, []uint32{2, 3, 0, 2, 0, 1}, indices)
	require.Zero(t, Deviation(points, nil, indices))
}

func TestRefinePreservesConcavePolygon(t *testing.T) {
	polygon := []Point[float64]{{0, 0}, {4, 0}, {4, 1}, {1, 1}, {1, 4}, {0, 4}}

	_, indices := Triangulate(polygon, nil)
	count := len(indices)

	before := trianglePerimeter(indices, polygon)
	Refine(indices, polygon)
	after := trianglePerimeter(indices, polygon)

	require.Len(t, indices, count)
	require.Less(t, after, before*0.9)
	require.Zero(t, Deviation(polygon, nil, indices))
}

func TestRefineTerminatesOnNearCocircularPoints(t *testing.T) {
	// four near-cocircular points make inCircle give inconsistent signs for an edge and its
	// flip. Without the tie margin, Refine flips one edge back and forth forever.
	polygon := []Point[float64]{
		{127.65906365022843, 9.336137742499535},
		{124.21725103117963, 30.888097161477972},
		{91.35514946628345, 89.65621376119454},
		{40.10446780041529, 121.5550560957686},
		{-110.83205604043928, 64.03323632184248},
		{-127.20394987965459, -14.253249980770189},
		{61.074962259031416, -112.48932831632469},
		{127.37846573978545, -12.598669206638515},
		{127.77010311801033, -7.668164657400608},
	}

	_, indices := Triangulate(polygon, nil)
	count := len(indices)

	Refine(indices, polygon)

	require.Len(t, indices, count)
	require.Less(t, Deviation(polygon, nil, indices), 1e-15)
}

func TestRefineLegalizesEarcutFixture(t *testing.T) {
	outer, holes := toPolygon(loadFixture(t, "earcut"))

	points, indices := Triangulate(outer, holes)
	Refine(indices, points)

	require.Zero(t, countIllegalEdges(indices, points))
	require.Zero(t, Deviation(outer, holes, indices))
}

func TestTilesFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}

	polygons := readTilesFixture(t)
	require.Len(t, polygons, 119680)

	var nonzero, refinedNonzero, countChanged int
	var basePerimeter, refinedPerimeter float64

	for i, p := range polygons {
		points, indices := Triangulate(p.outer, p.holes)
		count := len(indices)

		basePerimeter += trianglePerimeter(indices, points)
		if dev := Deviation(p.outer, p.holes, indices); dev != 0 {
			if nonzero == 0 {
				t.Logf("first polygon with nonzero deviation: %d: %g", i, dev)
			}

			nonzero++
		}

		Refine(indices, points)
		refinedPerimeter += trianglePerimeter(indices, points)

		if len(indices) != count {
			countChanged++
		}

		if dev := Deviation(p.outer, p.holes, indices); dev != 0 {
			if refinedNonzero == 0 {
				t.Logf("first refined polygon with nonzero deviation: %d: %g", i, dev)
			}

			refinedNonzero++
		}
	}

	require.Zero(t, nonzero, "polygons with nonzero deviation")
	require.Zero(t, countChanged, "refined triangulations with a different triangle count")
	require.Zero(t, refinedNonzero, "refined polygons with nonzero deviation")
	require.Less(t, refinedPerimeter, basePerimeter*0.72)
}

// Regression for the hole bridge block index: a collinear outer ring on an integer grid plus
// multiple holes used to drop a hole when filterPoints healed a collinear run across a block
// boundary, leaving the new edge outside its block's old bbox.
func TestBlockIndexCollinear(t *testing.T) {
	const n = 30

	var outer [][2]float64
	for x := 0; x <= n; x++ {
		outer = append(outer, [2]float64{float64(x), 0})
	}
	for y := 1; y <= n; y++ {
		outer = append(outer, [2]float64{n, float64(y)})
	}
	for x := n - 1; x >= 0; x-- {
		outer = append(outer, [2]float64{float64(x), n})
	}
	for y := n - 1; y >= 1; y-- {
		outer = append(outer, [2]float64{0, float64(y)})
	}

	rect := func(x0, y0, w, h float64) [][2]float64 {
		return [][2]float64{{x0, y0}, {x0, y0 + h}, {x0 + w, y0 + h}, {x0 + w, y0}}
	}

	rings := [][][2]float64{outer, rect(5, 5, 2, 4), rect(2, 23, 1, 1)}

	for _, rotation := range []int{0, 90, 180, 270} {
		outer, holes := toPolygon(rotate(rings, rotation))
		_, indices := Triangulate(outer, holes)
		require.Less(t, Deviation(outer, holes, indices), 1e-9, "rotation %d", rotation)
	}
}

func trianglePerimeter[T float](indices []uint32, points []Point[T]) float64 {
	var perimeter float64

	for i := 0; i < len(indices); i += 3 {
		a, b, c := points[indices[i]], points[indices[i+1]], points[indices[i+2]]

		perimeter += math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y)) +
			math.Hypot(float64(b.X-c.X), float64(b.Y-c.Y)) +
			math.Hypot(float64(c.X-a.X), float64(c.Y-a.Y))
	}

	return perimeter
}

// countIllegalEdges counts interior edges of convex quads that are not Delaunay
func countIllegalEdges[T float](indices []uint32, points []Point[T]) int {
	halfEdges := make([]int, len(indices))
	edges := map[[2]uint32]int{}

	for e := range indices {
		a, b := indices[e], indices[nextHalfEdge(e)]
		key := [2]uint32{min(a, b), max(a, b)}

		if twin, ok := edges[key]; ok {
			halfEdges[e] = twin
			halfEdges[twin] = e
			delete(edges, key)
		} else {
			halfEdges[e] = -1
			edges[key] = e
		}
	}

	pointAt := func(idx uint32) (float64, float64) {
		return float64(points[idx].X), float64(points[idx].Y)
	}

	var illegal int
	for a, b := range halfEdges {
		if b == -1 || a > b {
			continue
		}

		a0 := a - a%3
		b0 := b - b%3
		ar := a0 + (a+2)%3
		al := a0 + (a+1)%3
		bl := b0 + (b+2)%3

		x0, y0 := pointAt(indices[ar])
		xr, yr := pointAt(indices[a])
		xl, yl := pointAt(indices[al])
		x1, y1 := pointAt(indices[bl])

		convex := orient(x0, y0, xr, yr, x1, y1) > 0 && orient(x0, y0, x1, y1, xl, yl) > 0
		if convex && !strictInCircle(x0, y0, xr, yr, xl, yl, x1, y1) {
			illegal++
		}
	}

	return illegal
}

// inCircle without the tie margin
func strictInCircle(ax, ay, bx, by, cx, cy, px, py float64) bool {
	dx, dy := ax-px, ay-py
	ex, ey := bx-px, by-py
	fx, fy := cx-px, cy-py

	//goland:noinspection GoRedundantConversion
	ap := float64(dx*dx) + float64(dy*dy)
	//goland:noinspection GoRedundantConversion
	bp := float64(ex*ex) + float64(ey*ey)
	//goland:noinspection GoRedundantConversion
	cp := float64(fx*fx) + float64(fy*fy)

	//goland:noinspection GoRedundantConversion
	det := float64(dx*(float64(ey*cp)-float64(bp*fy))) -
		float64(dy*(float64(ex*cp)-float64(bp*fx))) +
		float64(ap*(float64(ex*fy)-float64(ey*fx)))

	return det <= 0
}
