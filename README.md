# earcut-go

earcut-go splits a polygon, with optional holes, into triangles. It is a Go port of the JavaScript library
[mapbox/earcut](https://github.com/mapbox/earcut).

The port follows upstream commit
[77019d3](https://github.com/mapbox/earcut/tree/77019d32d4fec70758ba13b52ce6692e9b4b1246) from 2026-10-18, which
is newer than release 3.2.4. On arm64, it returns the same triangles as the JavaScript version for all upstream test
polygons and for the 119,680 polygons in the upstream tile fixture. Other architectures were not compared.

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

`Triangulate` takes the outer ring and a list of holes, with `float32` or `float64` points. The rings can be in
either winding order (clockwise or counterclockwise).

It returns two slices. The first holds the outer ring followed by all holes. If there are no holes, it is the
polygon slice itself, not a copy. The second holds indices into the first, and every three indices form one triangle.

A hole with a single point is a Steiner point: `Triangulate` does not cut it out, but uses it as a triangle vertex.

`Deviation` compares the area of the polygon with the total area of the triangles. A result near zero means the
triangulation is correct.

`Refine` flips edges of a triangulation in place until it is close to the constrained Delaunay triangulation (of
all triangulations of the polygon, the one with the largest smallest angle). Most long, thin triangles disappear. If
you use the triangles for shading or physics, where thin triangles cause problems, run `Refine` on the result:

```go
points, indices := earcut.Triangulate(polygon, holes)
earcut.Refine(indices, points)
```

## Differences from upstream

- The API takes rings of points instead of a flat coordinate array with hole indices. There is no `flatten`
  function and no `dim` parameter.
- `Triangulate` ignores empty holes. Upstream skips them in `flatten`.
- Upstream keeps scratch buffers in module level variables. This port creates them for each call, so you can call
  `Triangulate` from multiple goroutines at the same time.

## The algorithm

The library uses ear slicing: it repeatedly cuts off a triangle at a convex corner of the polygon. A
[z-order curve](https://en.wikipedia.org/wiki/Z-order_curve) index (a way to sort points so that points close in
space are close in the list) finds the points near a candidate triangle.

It also accepts polygons with self-intersections, duplicate points and zero-area parts. For such input it does not
guarantee a correct triangulation, but tries to return a usable result for real-world data.

It is based on ideas from
[FIST: Fast Industrial-Strength Triangulation of Polygons](http://www.cosy.sbg.ac.at/~held/projects/triang/triang.html)
by Martin Held and [Triangulation by Ear Clipping](http://www.geometrictools.com/Documentation/TriangulationByEarClipping.pdf)
by David Eberly. The [upstream README](https://github.com/mapbox/earcut/blob/77019d32d4fec70758ba13b52ce6692e9b4b1246/README.md)
has more details.

## Tests

The tests use the polygons and expected results from upstream, stored in `testdata`. `testdata/tiles-fixture.bin` holds
119,680 polygons from Mapbox Vector Tiles (the binary map tile format Mapbox uses). The test for this file takes about
a second, and `go test -short` skips it.
