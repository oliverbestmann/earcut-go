# earcut-go

A Go port of the JavaScript polygon triangulation library [mapbox/earcut](https://github.com/mapbox/earcut).
It splits a polygon, with optional holes, into triangles.

The port follows upstream commit
[77019d3](https://github.com/mapbox/earcut/tree/77019d32d4fec70758ba13b52ce6692e9b4b1246) (after version 3.2.4).
For the upstream fixtures and the upstream tile fixture with 119,680 polygons, it returns exactly the same triangles
as the JavaScript version.

## Usage

```go
import "github.com/oliverbestmann/earcut-go"

polygon := []earcut.Point[float64]{
	{X: 0, Y: 0},
	{X: 10, Y: 0},
	{X: 10, Y: 10},
	{X: 0, Y: 10},
}

points, indices := earcut.Triangulate(polygon, nil)
// points[indices[0]], points[indices[1]], points[indices[2]] form the first triangle,
// points[indices[3]], points[indices[4]], points[indices[5]] the second one, and so on.
```

`Triangulate` takes the outer ring and a list of holes. It works with `float32` and `float64` points. It returns the
outer ring and the holes concatenated into one slice, and a flat slice of indices into that slice. Every three indices
form one triangle. A hole with a single point is a Steiner point: it is not cut out, but becomes a vertex of the
triangulation. The winding order of the input does not matter.

`Deviation` compares the area of the polygon with the area of the triangles. A result near zero means the
triangulation is correct.

`Refine` flips edges of a triangulation in place until it is close to the constrained Delaunay triangulation (the
triangulation that maximizes the smallest angle of all triangles). The output has fewer long, thin triangles. Use it if
the triangles are used for things like shading or physics, where thin triangles cause problems.

```go
points, indices := earcut.Triangulate(polygon, holes)
earcut.Refine(indices, points)
```

## Differences to upstream

- The API takes rings of points instead of a flat coordinate array with hole indices, so there is no `flatten` and
  no `dim` parameter.
- Empty holes are ignored. Upstream skips them in `flatten`.
- Upstream keeps scratch buffers in module level variables. This port allocates them for each call, so
  `Triangulate` is safe to call from multiple goroutines.

## The algorithm

The library uses ear slicing (repeatedly cutting off a triangle at a convex corner of the polygon). It uses a
[z-order curve](https://en.wikipedia.org/wiki/Z-order_curve) index to find points near a candidate triangle quickly.
It also handles holes, twisted polygons, degenerate input and self-intersections. It does not guarantee a correct
triangulation for such input, but tries to produce an acceptable result for real-world data.

It is based on ideas from
[FIST: Fast Industrial-Strength Triangulation of Polygons](http://www.cosy.sbg.ac.at/~held/projects/triang/triang.html)
by Martin Held and [Triangulation by Ear Clipping](http://www.geometrictools.com/Documentation/TriangulationByEarClipping.pdf)
by David Eberly. See the [upstream README](https://github.com/mapbox/earcut/blob/77019d32d4fec70758ba13b52ce6692e9b4b1246/README.md)
for more details.

## Tests

The tests use the fixtures and expected results from upstream, in `testdata`. `testdata/tiles-fixture.bin` holds
119,680 polygons from Mapbox Vector Tiles. The test for it takes about a second, and `go test -short` skips it.
