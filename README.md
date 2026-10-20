# Earcut

## About the port

This package is a straightforward Go port of the JavaScript library [mapbox/earcut](https://github.com/mapbox/earcut).
It mirrors the original behaviour and aims to be a drop-in equivalent for polygon triangulation.

The code is a manual port. Documentation and comments were AI-generated or refined.

The API is intentionally small. There is one exported function, `Triangulate`, and one exported type, `Point[T]`, which is used to represent polygon vertices. `Triangulate` takes a slice of points forming the outer polygon and an optional list of hole polygons. It returns two values: the outer ring and all holes concatenated into one slice, and a flat slice of indices into that slice. Every consecutive group of three indices references one triangle in the output.

Signature:

```go
func Triangulate[T float32 | float64](polygon []Point[T], holes [][]Point[T]) ([]Point[T], []uint32)
```

Use `Triangulate` with either `float32` or `float64` by specifying the type parameter explicitly.

For algorithm details, edge cases, and general behaviour, refer to the original
[README](https://github.com/mapbox/earcut/blob/c00ba4e1e724cab5e611d136eef9d3f43e2f3386/README.md).

## Usage

```go
import "github.com/oliverbestmann/earcut-go"

// Define polygon vertices (using float64)
polygon := []earcut.Point[float64]{
    {X: 0, Y: 0},
    {X: 10, Y: 0},
    {X: 10, Y: 10},
    {X: 0, Y: 10},
}

points, indices := earcut.Triangulate(polygon, nil)
// indices[0], indices[1], indices[2] form the first triangle
// indices[3], indices[4], indices[5] form the second triangle, etc.
```

## The algorithm

The library implements a modified ear slicing algorithm,
optimized by [z-order curve](https://en.wikipedia.org/wiki/Z-order_curve) hashing
and extended to handle holes, twisted polygons, degeneracies and self-intersections
in a way that doesn't _guarantee_ correctness of triangulation,
but attempts to always produce acceptable results for practical data.

It's based on ideas from
[FIST: Fast Industrial-Strength Triangulation of Polygons](http://www.cosy.sbg.ac.at/~held/projects/triang/triang.html)
by Martin Held
and [Triangulation by Ear Clipping](http://www.geometrictools.com/Documentation/TriangulationByEarClipping.pdf) by David
Eberly.
