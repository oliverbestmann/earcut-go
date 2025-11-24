# Earcut

## About the port

This package is a straightforward Go port of the JavaScript library [mapbox/earcut](https://github.com/mapbox/earcut).
It mirrors the original behaviour and aims to be a drop-in equivalent for polygon triangulation.

The API is intentionally small. There is only one exported function, Triangulate, which takes a slice of points forming
the outer polygon and an optional list of hole polygons. The function processes these inputs and returns two values: the
set of points actually used in the triangulation, and a flat slice of indices. Every consecutive group of three indices
references one triangle in the output.

Signature:

```go
func Triangulate[T float](polygon []Point[T], holes [][]Point[T]) ([]Point[T], []uint32)
```

You can use Triangulate with either `float32` or `float64`.

For algorithm details, edge cases, and general behaviour, refer to the original
[README](https://github.com/mapbox/earcut/blob/c00ba4e1e724cab5e611d136eef9d3f43e2f3386/README.md).

## Usage

```go
import "github.com/oliverbestmann/earcut-go"

points := []earcut.Point{ /* ... */ }
points, indices := earcut.Triangulate(points, nil)
```

## The algorithm

The library implements a modified ear slicing algorithm,
optimized by [z-order curve](http://en.wikipedia.org/wiki/Z-order_curve) hashing
and extended to handle holes, twisted polygons, degeneracies and self-intersections
in a way that doesn't _guarantee_ correctness of triangulation,
but attempts to always produce acceptable results for practical data.

It's based on ideas from
[FIST: Fast Industrial-Strength Triangulation of Polygons](http://www.cosy.sbg.ac.at/~held/projects/triang/triang.html)
by Martin Held
and [Triangulation by Ear Clipping](http://www.geometrictools.com/Documentation/TriangulationByEarClipping.pdf) by David
Eberly.
