package earcut

import "sync"

// Refine changes a triangulation in place. It flips the shared edge of two neighboring
// triangles (a Lawson flip) until the result is close to the constrained Delaunay
// triangulation, which has the largest possible smallest angle. Most long, thin
// triangles disappear. The polygon boundary and the number of triangles stay the same.
//
// indices are the triangle indices returned by Triangulate, and points are the points
// they index into. Refine works on any manifold triangle mesh (a mesh where every edge
// belongs to at most two triangles).
//
// The geometric tests use plain floating point math. Because of rounding, an edge can
// stay slightly off from Delaunay. The mesh itself stays valid.
func Refine[T float](indices []uint32, points []Point[T]) {
	t := indices
	n := len(t) - len(t)%3
	if n < 6 {
		return
	}

	pointAt := func(idx uint32) (float64, float64) {
		p := points[idx]
		return float64(p.X), float64(p.Y)
	}

	// the predicates below assume counterclockwise triangles. For a clockwise mesh, swapping
	// each edge's endpoints mirrors them, so take the winding from the first non-degenerate triangle.
	cw := false
	for k := 0; k < n; k += 3 {
		ax, ay := pointAt(t[k])
		bx, by := pointAt(t[k+1])
		cx, cy := pointAt(t[k+2])

		o := orient(ax, ay, bx, by, cx, cy)
		cw = o < 0
		if o != 0 {
			break
		}
	}

	// open addressing hash table from undirected edge to half-edge, load factor <= 0.25.
	size := 1
	for size < n*4 {
		size <<= 1
	}

	scratch, _ := refinePool.Get().(*refineScratch)
	if scratch == nil {
		scratch = &refineScratch{}
	}
	defer refinePool.Put(scratch)

	if cap(scratch.ints) < 2*n+size {
		scratch.ints = make([]int32, 2*n+size)
	}

	if cap(scratch.stamps) < n {
		scratch.stamps = make([]bool, n)
	}

	ints := scratch.ints[:2*n+size]

	// twin half-edge of each edge, or -1 on the polygon boundary
	he := ints[:n]
	for i := range he {
		he[i] = -1
	}

	// interior edges still to check, and whether an edge is currently in the stack.
	// edgeStamp keeps at most one entry per half-edge on the stack, so n is enough.
	edgeStack := ints[n : 2*n]
	edgeStamp := scratch.stamps[:n]
	clear(edgeStamp)

	// a slot value of -2 is empty, -1 marks a pair that was already linked
	hTable := ints[2*n:]
	for i := range hTable {
		hTable[i] = -2
	}

	hMask := uint32(size - 1)

	// link the half-edge twins. Each linked pair seeds the stack with one of its edges.
	sp := 0
	for e := range n {
		a, b := t[e], t[nextHalfEdge(e)]
		lo, hi := min(a, b), max(a, b)

		h := (lo*0x9e3779b1 ^ hi*0x85ebca6b) & hMask
		for hTable[h] != -2 {
			if s := hTable[h]; s != -1 {
				sa, sb := t[s], t[nextHalfEdge(int(s))]
				if (sa == lo && sb == hi) || (sa == hi && sb == lo) {
					// link, then consume the slot
					he[e] = s
					he[s] = int32(e)
					hTable[h] = -1

					// seed the interior edge for the cascade
					edgeStamp[s] = true
					edgeStack[sp] = s
					sp++
					break
				}
			}

			h = (h + 1) & hMask
		}

		if hTable[h] == -2 {
			// first occurrence: insert
			hTable[h] = int32(e)
		}
	}

	push := func(edge int32) {
		if he[edge] != -1 && !edgeStamp[edge] {
			edgeStamp[edge] = true
			edgeStack[sp] = edge
			sp++
		}
	}

	for sp > 0 {
		sp--
		a := edgeStack[sp]
		edgeStamp[a] = false

		b := he[a]
		if b == -1 {
			continue
		}

		a0 := a - a%3
		b0 := b - b%3
		ar := a0 + (a+2)%3
		al := a0 + (a+1)%3
		bl := b0 + (b+2)%3
		br := b0 + (b+1)%3

		p0, p1 := t[ar], t[bl]
		pr, pl := t[a], t[al]
		if cw {
			pr, pl = pl, pr
		}

		x0, y0 := pointAt(p0)
		xr, yr := pointAt(pr)
		xl, yl := pointAt(pl)
		x1, y1 := pointAt(p1)

		// Most interior edges are already Delaunay, so test inCircle first. The quad must also be
		// convex (both new triangles counterclockwise): flipping a reflex quad would push a triangle
		// outside the polygon. Boundary edges are never on the stack (he == -1).
		if !inCircle(x0, y0, xr, yr, xl, yl, x1, y1) &&
			orient(x0, y0, xr, yr, x1, y1) > 0 && orient(x0, y0, x1, y1, xl, yl) > 0 {

			t[a] = p1
			t[b] = p0

			hbl, har := he[bl], he[ar]

			he[a] = hbl
			if hbl != -1 {
				he[hbl] = a
			}

			he[b] = har
			if har != -1 {
				he[har] = b
			}

			he[ar] = bl
			he[bl] = ar

			// re-check the four outer edges of the quad
			push(a)
			push(b)
			push(al)
			push(br)
		}
	}
}

// refinePool keeps the scratch buffers of Refine for reuse by the next call
var refinePool sync.Pool

type refineScratch struct {
	ints   []int32
	stamps []bool
}

// next half-edge within the same triangle
func nextHalfEdge(e int) int {
	return e - e%3 + (e+1)%3
}

func orient(ax, ay, bx, by, cx, cy float64) float64 {
	// do not remove the extra float64 casts, they prevent go from emitting fma instructions
	//goland:noinspection GoRedundantConversion
	return float64((bx-ax)*(cy-ay)) - float64((by-ay)*(cx-ax))
}

// inCircle reports whether p is inside or on the circumcircle of triangle (a, b, c). The sign
// is negated compared to the usual predicate to match the counterclockwise winding. Cocircular
// quads are legal ties, so Refine only flips when this returns false.
func inCircle(ax, ay, bx, by, cx, cy, px, py float64) bool {
	dx, dy := ax-px, ay-py
	ex, ey := bx-px, by-py
	fx, fy := cx-px, cy-py

	// do not remove the extra float64 casts, they prevent go from emitting fma instructions
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

	// Rounding can make both an edge and its flip look illegal for a near-cocircular quad,
	// which would flip forever. The determinant's rounding error stays below
	// 9e-16 * (ap + bp + cp)^2, so a margin of 1e-13 * s^2 makes every flip legal in exact
	// arithmetic, and flipping always terminates.
	s := ap + bp + cp
	return det <= 1e-13*s*s
}
