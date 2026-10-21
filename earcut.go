// Package earcut implements polygon triangulation using a modified
// ear-clipping algorithm optimized with Z-order curve hashing.
// It supports holes, self-intersections, and degenerate inputs,
// and is a Go port of mapbox/earcut.
package earcut

import (
	"math"
	"slices"
	"sync"
)

// Point is a 2D point with X and Y coordinates.
type Point[T float] struct {
	X, Y T
}

type float interface {
	float32 | float64
}

// Triangulate triangulates a polygon with possible holes using the earcut algorithm.
//
// The polygon parameter defines the outer boundary; each element of holes
// defines an inner boundary (a hole) within the polygon.
//
// It returns two slices. The first holds the outer ring followed by all holes. If
// there are no holes, it is the polygon slice itself, not a copy. The second holds
// indices into the first, and every three consecutive indices form one triangle:
// indices[i], indices[i+1], indices[i+2].
//
// A hole with a single point is a Steiner point: Triangulate does not cut it out,
// but uses it as a triangle vertex. Empty holes are ignored.
func Triangulate[T float](polygon []Point[T], holes [][]Point[T]) ([]Point[T], []uint32) {
	points := concatRings(polygon, holes)

	e, _ := statePool.Get().(*earcut[T])
	if e == nil {
		// shortcut: one pool for all T, mixing float32 and float64 calls drops states. Fine unless both are hot.
		e = &earcut[T]{}
	}
	defer statePool.Put(e)

	e.reset(len(points) + 2*len(holes))

	outer := e.linkedList(polygon, 0, true)
	if outer == nil || outer.Next == outer.Prev {
		return points, nil
	}

	if slices.ContainsFunc(holes, func(hole []Point[T]) bool { return len(hole) > 0 }) {
		outer = e.eliminateHoles(outer, holes, uint32(len(polygon)), len(points))
	}

	// if the shape is not too simple, we'll use z-order curve hash later; calculate polygon bbox.
	// like upstream, only the outer ring is used for the bbox.
	if len(points) > 80 {
		minX, minY := polygon[0].X, polygon[0].Y
		maxX, maxY := minX, minY

		for _, p := range polygon[1:] {
			minX = min(minX, p.X)
			minY = min(minY, p.Y)
			maxX = max(maxX, p.X)
			maxY = max(maxY, p.Y)
		}

		// minX, minY and invSize are later used to transform coords into integers for z-order calculation
		e.minX, e.minY = minX, minY
		if size := max(maxX-minX, maxY-minY); size != 0 {
			e.invSize = 32767 / size
		}
	}

	// every hole adds two bridge nodes, and so two triangles
	e.triangles = make([]uint32, 0, 3*(len(points)+2*len(holes)))

	e.earcutLinked(outer)
	for len(e.splitStack) > 0 {
		last := len(e.splitStack) - 1
		ear := e.splitStack[last]
		e.splitStack = e.splitStack[:last]
		e.earcutLinked(ear)
	}

	return points, e.triangles
}

// statePool keeps earcut states, so the node and scratch buffers of one call are reused by the next.
// Nodes never escape a call, so reusing them is safe.
var statePool sync.Pool

// reset prepares a pooled state for a new call, keeping the buffers.
func (e *earcut[T]) reset(expectedNodes int) {
	*e = earcut[T]{
		alloc:      e.alloc,
		splitStack: e.splitStack[:0],
		blocks:     e.blocks[:0],
		sortArr:    e.sortArr[:0],
		sortBuf:    e.sortBuf,
		zArr:       e.zArr,
		zBuf:       e.zBuf,
	}

	e.alloc.reset(expectedNodes)
}

func concatRings[T float](polygon []Point[T], holes [][]Point[T]) []Point[T] {
	count := len(polygon)
	for _, hole := range holes {
		count += len(hole)
	}

	if count == len(polygon) {
		// no holes, no need to copy
		return polygon
	}

	points := make([]Point[T], 0, count)
	points = append(points, polygon...)
	for _, hole := range holes {
		points = append(points, hole...)
	}

	return points
}

// earcut holds the state of a single Triangulate call. Upstream keeps this in module level variables.
type earcut[T float] struct {
	alloc     nodeAllocator[T]
	triangles []uint32

	// z-order hashing is used if invSize is not zero
	minX, minY, invSize T

	// halves of split polygons waiting to be triangulated, so the call stack does not
	// grow with the nesting depth of splits
	splitStack []*node[T]

	// set by filterPoints whenever it removes at least one node
	filteredOut bool

	// true only while eliminateHoles merges holes, so removeNode keeps the block index live
	indexActive bool

	// block index for findHoleBridge, see indexSegment
	blocks []block[T]

	// scratch buffers for sorting nodes by z-order
	sortArr, sortBuf []*node[T]
	zArr, zBuf       []uint32
}

// block is a run of up to blockSize consecutive ring edges in the hole bridge index
type block[T float] struct {
	// first node of the run, and the node just past it
	head, stop *node[T]

	// bbox of all edges in the run
	minX, minY, maxX, maxY T
}

// create a circular doubly linked list from polygon points in the specified winding order
func (e *earcut[T]) linkedList(points []Point[T], indexOffset uint32, clockwise bool) *node[T] {
	var last *node[T]

	if clockwise == (signedArea(points) > 0) {
		for i, point := range points {
			last = e.insertNode(uint32(i)+indexOffset, point, last)
		}
	} else {
		for i := len(points) - 1; i >= 0; i-- {
			last = e.insertNode(uint32(i)+indexOffset, points[i], last)
		}
	}

	if last != nil && last.EqualTo(last.Next) {
		e.removeNode(last)
		last = last.Next
	}

	return last
}

// filterPoints removes collinear or coincident points. If end equals start, it sweeps the
// whole ring until nothing can be removed. Otherwise it only heals the window between start
// and end around a bridge or diagonal cut.
func (e *earcut[T]) filterPoints(start, end *node[T]) *node[T] {
	full := end == start

	p := start
	for {
		again := false

		if p != p.Next && !p.Steiner && (p.EqualTo(p.Next) || area(p.Prev, p, p.Next) == 0) {
			if full || p == end {
				// pull the stop bound back past the removal
				end = p.Prev
			}

			e.filteredOut = true
			e.removeNode(p)

			// re-check the predecessor
			p = p.Prev
			again = true

		} else if full || p != end {
			p = p.Next

			// local heal: keep looping until the sweep reaches end
			again = !full
		}

		if !again && p == end {
			return end
		}
	}
}

// main ear slicing loop which triangulates a polygon (given as a linked list)
func (e *earcut[T]) earcutLinked(ear *node[T]) {
	hashed := e.invSize != 0

	// interlink polygon nodes in z-order
	if hashed {
		e.indexCurve(ear)
	}

	// a strictly convex ring has no vertex that could block an ear, so skip the point checks
	// until a candidate fails. Without this, a big convex ring is cut as one fan of long
	// triangles and each hashed check scans O(n) points.
	stop := ear
	cured := false
	convex := hashed && isConvex(ear)

	// iterate through ears, slicing them one by one
	for ear.Prev != ear.Next {
		prev, next := ear.Prev, ear.Next

		if area(prev, ear, next) < 0 && (convex || (hashed && e.isEarHashed(ear)) || (!hashed && isEar(ear))) {
			// cut off the triangle
			e.triangles = append(e.triangles, prev.Index, ear.Index, next.Index)

			e.removeNode(ear)
			ear = next
			stop = next
			continue
		}

		convex = false
		ear = next

		// if we looped through the whole remaining polygon and can't find any more ears
		if ear == stop {
			// try filtering collinear/coincident points and slicing again. Repeat as long as
			// filtering removes nodes, since each removal can expose new ears.
			e.filteredOut = false
			ear = e.filterPoints(ear, ear)
			if e.filteredOut {
				stop = ear
				continue
			}

			// filtering is exhausted: cure small local self-intersections once, then retry
			if !cured {
				ear = e.cureLocalIntersections(ear)
				stop = ear
				cured = true
				continue
			}

			// as a last resort, try splitting the remaining polygon into two
			e.splitEarcut(ear)
			break
		}
	}
}

// check whether every vertex of a ring is strictly convex
func isConvex[T float](start *node[T]) bool {
	p := start
	for {
		if area(p.Prev, p, p.Next) >= 0 {
			return false
		}

		p = p.Next
		if p == start {
			return true
		}
	}
}

// check whether a polygon node forms a valid ear with adjacent nodes.
// The reflex check is done by the caller.
func isEar[T float](ear *node[T]) bool {
	a, b, c := ear.Prev, ear, ear.Next

	// triangle bbox
	x0 := min(a.X, b.X, c.X)
	y0 := min(a.Y, b.Y, c.Y)
	x1 := max(a.X, b.X, c.X)
	y1 := max(a.Y, b.Y, c.Y)

	// make sure we don't have other points inside the potential ear
	for p := c.Next; p != a; p = p.Next {
		if p.X >= x0 && p.X <= x1 && p.Y >= y0 && p.Y <= y1 && a.Point != p.Point &&
			pointInTriangle(a.Point, b.Point, c.Point, p.Point) && area(p.Prev, p, p.Next) >= 0 {
			return false
		}
	}

	return true
}

// like isEar, but uses the z-order index to only check nearby points
func (e *earcut[T]) isEarHashed(ear *node[T]) bool {
	a, b, c := ear.Prev, ear, ear.Next

	// triangle bbox
	x0 := min(a.X, b.X, c.X)
	y0 := min(a.Y, b.Y, c.Y)
	x1 := max(a.X, b.X, c.X)
	y1 := max(a.Y, b.Y, c.Y)

	// z-order range for the current triangle bbox
	minZ := e.zOrder(x0, y0)
	maxZ := e.zOrder(x1, y1)

	// look for points inside the triangle in decreasing z-order
	for p := ear.PrevZ; p != nil && p.Z >= minZ; p = p.PrevZ {
		if p.X >= x0 && p.X <= x1 && p.Y >= y0 && p.Y <= y1 && p != c && a.Point != p.Point &&
			pointInTriangle(a.Point, b.Point, c.Point, p.Point) && area(p.Prev, p, p.Next) >= 0 {
			return false
		}
	}

	// look for points in increasing z-order
	for n := ear.NextZ; n != nil && n.Z <= maxZ; n = n.NextZ {
		if n.X >= x0 && n.X <= x1 && n.Y >= y0 && n.Y <= y1 && n != c && a.Point != n.Point &&
			pointInTriangle(a.Point, b.Point, c.Point, n.Point) && area(n.Prev, n, n.Next) >= 0 {
			return false
		}
	}

	return true
}

// go through all polygon nodes and cure small local self-intersections
func (e *earcut[T]) cureLocalIntersections(start *node[T]) *node[T] {
	p := start
	cured := false

	for {
		a := p.Prev
		b := p.Next.Next

		if intersects(a, p, p.Next, b, false) && locallyInside(a, b) && locallyInside(b, a) {
			e.triangles = append(e.triangles, a.Index, p.Index, b.Index)

			// remove two nodes involved
			e.removeNode(p)
			e.removeNode(p.Next)

			p = b
			start = b
			cured = true
		}

		p = p.Next
		if p == start {
			break
		}
	}

	if cured {
		return e.filterPoints(p, p)
	}

	return p
}

// try splitting polygon into two and triangulate them independently
func (e *earcut[T]) splitEarcut(start *node[T]) {
	// look for a valid diagonal that divides the polygon into two
	a := start
	for {
		for b := a.Next.Next; b != a.Prev; b = b.Next {
			if a.Index != b.Index && isValidDiagonal(a, b) {
				// split the polygon in two by the diagonal
				c := e.splitPolygon(a, b)

				// filter collinear points around the cuts
				a = e.filterPoints(a, a.Next)
				c = e.filterPoints(c, c.Next)

				if e.splitStack == nil {
					e.splitStack = make([]*node[T], 0, 16)
				}

				// queue both halves, a on top so it's triangulated first
				e.splitStack = append(e.splitStack, c, a)
				return
			}
		}

		a = a.Next
		if a == start {
			return
		}
	}
}

// link every hole into the outer loop, producing a single-ring polygon without holes
func (e *earcut[T]) eliminateHoles(outer *node[T], holes [][]Point[T], indexOffset uint32, maxNodes int) *node[T] {
	queue := make([]*node[T], 0, len(holes))

	for _, hole := range holes {
		if len(hole) == 0 {
			continue
		}

		list := e.linkedList(hole, indexOffset, false)
		if list == list.Next {
			list.Steiner = true
		}

		queue = append(queue, getLeftmost(list))
		indexOffset += uint32(len(hole))
	}

	slices.SortStableFunc(queue, compareXYSlope)

	// seed the block index with the outer ring, each merged hole is appended later
	e.buildBlockIndex(maxNodes, len(queue))
	e.indexSegment(outer, outer)

	// process holes from left to right
	e.indexActive = true
	for _, hole := range queue {
		outer = e.eliminateHole(hole, outer)
	}
	e.indexActive = false

	// collapse collinear/coincident points across the whole merged ring once before clipping
	return e.filterPoints(outer, outer)
}

func compareXYSlope[T float](a, b *node[T]) int {
	// when the left-most point of 2 holes meet at a vertex, sort the holes counterclockwise so that when we find
	// the bridge to the outer shell is always the point that they meet at.
	// NaN is treated like zero, as in javascript.
	result := a.X - b.X
	if result == 0 || result != result {
		result = a.Y - b.Y
	}

	if result == 0 || result != result {
		result = (a.Next.Y-a.Y)/(a.Next.X-a.X) - (b.Next.Y-b.Y)/(b.Next.X-b.X)
	}

	return sign(result)
}

func sign[T float](value T) int {
	switch {
	case value > 0:
		return 1
	case value < 0:
		return -1
	default:
		return 0
	}
}

// find a bridge between vertices that connects hole with an outer ring and link it
func (e *earcut[T]) eliminateHole(hole, outer *node[T]) *node[T] {
	bridge := e.findHoleBridge(hole, outer)
	if bridge == nil {
		return outer
	}

	bridgeReverse := e.splitPolygon(bridge, hole)

	// index the merged-in segment before filtering. In ring order the splice runs
	// bridge -> hole -> bridgeReverse -> bridge2 -> (bridge's old next).
	bridge2 := bridgeReverse.Next
	e.indexSegment(bridge, bridge2.Next)

	// heal collinear/coincident points around the two new slit edges, but leave bridge2 alone: dropping it as
	// collinear would leave bridge lying on the healed edge, where a later hole could bridge into it from outside
	// its sector
	e.filterPoints(bridgeReverse.Prev, bridgeReverse)
	return e.filterPoints(bridge, bridge.Next)
}

// Block index for findHoleBridge: one bbox per blockSize consecutive ring edges, so the
// leftward-ray scan can skip whole blocks instead of walking the entire merged ring.
// The outer ring is indexed first, then each merged hole appends its own segment.
//
// filterPoints only drops collinear/coincident points, so an old bbox still covers its
// live edges. The scan skips removed nodes. Blocks are scanned in append order, not ring
// order, which can pick a different (but valid) bridge than a plain ring walk.
const blockSize = 16

func (e *earcut[T]) buildBlockIndex(maxNodes, numHoles int) {
	// every input node indexed once, +2 bridge nodes per hole, plus a partial
	// trailing block per appended segment
	maxBlocks := (maxNodes+2*numHoles+blockSize-1)/blockSize + numHoles + 2
	if cap(e.blocks) < maxBlocks {
		e.blocks = make([]block[T], 0, maxBlocks)
	}
}

// index the ring run head..stop (exclusive) as blocks of up to blockSize edges.
// If head equals stop, the whole ring is indexed.
func (e *earcut[T]) indexSegment(head, stop *node[T]) {
	p := head
	for {
		idx := int32(len(e.blocks))
		first := p

		minX, minY, maxX, maxY := p.X, p.Y, p.X, p.Y

		for k := 0; ; {
			// edge p->c, the bbox must cover both endpoints
			c := p.Next

			// reuse z as the owning block during eliminateHoles, see growBlock
			p.Z = idx

			minX = min(minX, c.X)
			minY = min(minY, c.Y)
			maxX = max(maxX, c.X)
			maxY = max(maxY, c.Y)

			p = c

			k++
			if k >= blockSize || p == stop {
				break
			}
		}

		e.blocks = append(e.blocks, block[T]{
			head: first, stop: p,
			minX: minX, minY: minY, maxX: maxX, maxY: maxY,
		})

		if p == stop {
			return
		}
	}
}

// when filterPoints heals an edge head->tail by removing the node between them, the new
// edge can reach outside of the bbox of head's block. Grow the bbox to cover tail.
func (e *earcut[T]) growBlock(head, tail *node[T]) {
	b := &e.blocks[head.Z]
	b.minX = min(b.minX, tail.X)
	b.minY = min(b.minY, tail.Y)
	b.maxX = max(b.maxX, tail.X)
	b.maxY = max(b.maxY, tail.Y)
}

func (b *block[T]) liveStop() *node[T] {
	for !b.stop.alive() {
		b.stop = b.stop.Next
	}

	return b.stop
}

// the head node of a block can be removed by filterPoints during merges. Advance it to
// the next live node so the walk does not start on a removed node.
func (b *block[T]) liveHead() *node[T] {
	for !b.head.alive() {
		b.head = b.head.Next
	}

	return b.head
}

// David Eberly's algorithm for finding a bridge between hole and outer polygon
func (e *earcut[T]) findHoleBridge(hole, outer *node[T]) *node[T] {
	hx, hy := hole.X, hole.Y
	qx := T(math.Inf(-1))

	var m *node[T]

	// find a segment intersected by a ray from the hole's leftmost point to the left;
	// segment's endpoint with lesser x will be potential connection point
	// unless they intersect at a vertex, then choose the vertex
	if hole.EqualTo(outer) {
		return outer
	}

	// skip blocks whose bbox can't hold a crossing that beats qx and lies left of hx
	for i := range e.blocks {
		b := &e.blocks[i]
		if hy < b.minY || hy > b.maxY || b.minX > hx || b.maxX <= qx {
			continue
		}

		stop := b.liveStop()

		p := b.liveHead()
		for {
			if hole.EqualTo(p.Next) {
				return p.Next
			}

			if hy <= p.Y && hy >= p.Next.Y && p.Next.Y != p.Y {
				x := p.X + (hy-p.Y)*(p.Next.X-p.X)/(p.Next.Y-p.Y)
				if x <= hx && x > qx {
					qx = x
					m = pick(p.X < p.Next.X, p, p.Next)

					if x == hx {
						// hole touches outer segment; pick leftmost endpoint
						return m
					}
				}
			}

			p = p.Next
			if p == stop {
				break
			}
		}
	}

	if m == nil {
		return nil
	}

	// look for points inside the triangle of hole point, segment intersection and endpoint;
	// if there are no points found, we have a valid connection;
	// otherwise choose the point of the minimum angle with the ray as connection point

	mx, my := m.X, m.Y

	// the triangle's y span; x span is [mx, hx]
	tminY := min(hy, my)
	tmaxY := max(hy, my)

	tanMin := T(math.Inf(1))

	t0 := Point[T]{X: pick(hy < my, hx, qx), Y: hy}
	t1 := Point[T]{X: mx, Y: my}
	t2 := Point[T]{X: pick(hy < my, qx, hx), Y: hy}

	// skip blocks whose bbox can't overlap the triangle's bbox
	for i := range e.blocks {
		b := &e.blocks[i]
		if b.maxX < mx || b.minX > hx || b.maxY < tminY || b.minY > tmaxY {
			continue
		}

		stop := b.liveStop()

		p := b.liveHead()
		for {
			if hx >= p.X && p.X >= mx && hx != p.X && pointInTriangle(t0, t1, t2, p.Point) {
				// tangential
				tan := T(math.Abs(float64(hy-p.Y))) / (hx - p.X)

				// if hole point sits on p's horizontal edge (T-junction touch): the bridge runs
				// along that edge. locallyInside rejects it as collinear, but it's valid.
				if (locallyInside(p, hole) || (p.Y == hy && p.Next.Y == hy && p.Next.X > hx)) &&
					(tan < tanMin || (tan == tanMin && (p.X > m.X || (p.X == m.X && sectorContainsSector(m, p))))) {
					m = p
					tanMin = tan
				}
			}

			p = p.Next
			if p == stop {
				break
			}
		}
	}

	return m
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}

	return b
}

// whether sector in vertex m contains sector in vertex p in the same coordinates
func sectorContainsSector[T float](m, p *node[T]) bool {
	return area(m.Prev, m, p.Prev) < 0 && area(p.Next, m, m.Next) < 0
}

// interlink polygon nodes in z-order: collect into a slice, sort by z, relink
func (e *earcut[T]) indexCurve(start *node[T]) {
	if cap(e.sortArr) < e.alloc.Expected {
		// the first ring is the largest one, it has at most the expected number of nodes
		// one allocation for both sortArr and sortBuf
		size := e.alloc.Expected
		buf := make([]*node[T], 2*size)
		e.sortArr, e.sortBuf = buf[:0:size], buf[size:]
	}

	nodes := e.sortArr[:0]

	p := start
	for {
		// always recompute: z may still hold a block index left over from eliminateHoles
		p.Z = e.zOrder(p.X, p.Y)
		nodes = append(nodes, p)

		p = p.Next
		if p == start {
			break
		}
	}

	e.sortArr = nodes
	e.sortNodes()

	var prev *node[T]
	for _, n := range e.sortArr {
		n.PrevZ = prev
		if prev != nil {
			prev.NextZ = n
		}

		prev = n
	}

	prev.NextZ = nil
}

// sort sortArr by z, in place: insertion sort for small slices, else LSD radix sort in
// four 8-bit passes. Like upstream, insertion sort compares z as signed and radix sort
// as unsigned. This only matters for points outside the outer ring's bbox.
func (e *earcut[T]) sortNodes() {
	nodes := e.sortArr
	n := len(nodes)

	if n <= 32 {
		for i := 1; i < n; i++ {
			node := nodes[i]

			j := i - 1
			for j >= 0 && nodes[j].Z > node.Z {
				nodes[j+1] = nodes[j]
				j--
			}

			nodes[j+1] = node
		}

		return
	}

	if len(e.zArr) < n {
		// size for the largest possible ring, so this only allocates once
		size := max(n, cap(e.sortArr))

		z := make([]uint32, 2*size)
		e.zArr, e.zBuf = z[:size], z[size:]

		if len(e.sortBuf) < size {
			e.sortBuf = make([]*node[T], size)
		}
	}

	zArr, zBuf, buf := e.zArr[:n], e.zBuf[:n], e.sortBuf[:n]
	for i, node := range nodes {
		zArr[i] = uint32(node.Z)
	}

	// even pass count lands the sorted result back in nodes
	radixPass(nodes, zArr, buf, zBuf, 0)
	radixPass(buf, zBuf, nodes, zArr, 8)
	radixPass(nodes, zArr, buf, zBuf, 16)
	radixPass(buf, zBuf, nodes, zArr, 24)
}

// one LSD radix pass: stably scatter the nodes (and their z) from src to dst,
// bucketed by the 8-bit digit of z at the given bit shift
func radixPass[T float](src []*node[T], srcZ []uint32, dst []*node[T], dstZ []uint32, shift uint) {
	var counts [256]uint32
	for _, z := range srcZ {
		counts[(z>>shift)&0xff]++
	}

	// turn per-bucket counts into start offsets (prefix sum)
	var sum uint32
	for b, c := range counts {
		counts[b] = sum
		sum += c
	}

	for i, z := range srcZ {
		digit := (z >> shift) & 0xff
		pos := counts[digit]
		counts[digit]++

		dst[pos] = src[i]
		dstZ[pos] = z
	}
}

// z-order of a point given coords and inverse of the longer side of data bbox.
// The integer math mirrors javascript's 32-bit integer semantics.
func (e *earcut[T]) zOrder(xf, yf T) int32 {
	// coords are transformed into non-negative 15-bit integer range
	x := toInt32((xf - e.minX) * e.invSize)
	y := toInt32((yf - e.minY) * e.invSize)

	x = (x | (x << 8)) & 0x00ff00ff
	x = (x | (x << 4)) & 0x0f0f0f0f
	x = (x | (x << 2)) & 0x33333333
	x = (x | (x << 1)) & 0x55555555

	y = (y | (y << 8)) & 0x00ff00ff
	y = (y | (y << 4)) & 0x0f0f0f0f
	y = (y | (y << 2)) & 0x33333333
	y = (y | (y << 1)) & 0x55555555

	return x | (y << 1)
}

// toInt32 works like javascript's "value | 0": truncate, then wrap to 32 bits
func toInt32[T float](value T) int32 {
	return int32(int64(value))
}

// find the leftmost node of a polygon ring
func getLeftmost[T float](start *node[T]) *node[T] {
	p := start
	leftmost := start

	for {
		if p.X < leftmost.X || (p.X == leftmost.X && p.Y < leftmost.Y) {
			leftmost = p
		}

		p = p.Next
		if p == start {
			return leftmost
		}
	}
}

// check if a point lies within a convex triangle
func pointInTriangle[T float](a, b, c, p Point[T]) bool {
	return (c.X-p.X)*(a.Y-p.Y) >= (a.X-p.X)*(c.Y-p.Y) &&
		(a.X-p.X)*(b.Y-p.Y) >= (b.X-p.X)*(a.Y-p.Y) &&
		(b.X-p.X)*(c.Y-p.Y) >= (c.X-p.X)*(b.Y-p.Y)
}

// check if a diagonal between two polygon nodes is valid (lies in polygon interior)
func isValidDiagonal[T float](a, b *node[T]) bool {
	// degenerate case
	zeroLength := a.EqualTo(b) && area(a.Prev, a, a.Next) > 0 && area(b.Prev, b, b.Next) > 0

	return a.Next.Index != b.Index &&
		// locally visible, no opposite-facing sectors
		(zeroLength || locallyInside(a, b) && locallyInside(b, a) &&
			(area(a.Prev, a, b.Prev) != 0 || area(a, b.Prev, b) != 0)) &&
		// doesn't intersect other edges, diagonal inside polygon
		!intersectsPolygon(a, b) && (zeroLength || middleInside(a, b))
}

// signed area of a triangle
func area[T float](p, q, r *node[T]) T {
	// do not remove the extra T casts, they prevent go from emitting fma instructions
	//goland:noinspection GoRedundantConversion
	return T((q.Y-p.Y)*(r.X-q.X)) - T((q.X-p.X)*(r.Y-q.Y))
}

// check if two segments intersect. If includeBoundary is set, collinear touches count.
func intersects[T float](p1, q1, p2, q2 *node[T], includeBoundary bool) bool {
	o1 := area(p1, q1, p2)
	o2 := area(p1, q1, q2)
	o3 := area(p2, q2, p1)
	o4 := area(p2, q2, q1)

	if ((o1 > 0 && o2 < 0) || (o1 < 0 && o2 > 0)) && ((o3 > 0 && o4 < 0) || (o3 < 0 && o4 > 0)) {
		return true
	}

	if !includeBoundary {
		return false
	}

	return o1 == 0 && onSegment(p1, p2, q1) || // p1, q1 and p2 are collinear and p2 lies on p1q1
		o2 == 0 && onSegment(p1, q2, q1) || // p1, q1 and q2 are collinear and q2 lies on p1q1
		o3 == 0 && onSegment(p2, p1, q2) || // p2, q2 and p1 are collinear and p1 lies on p2q2
		o4 == 0 && onSegment(p2, q1, q2) // p2, q2 and q1 are collinear and q1 lies on p2q2
}

// for collinear points p, q, r, check if point q lies on segment pr
func onSegment[T float](p, q, r *node[T]) bool {
	return q.X <= max(p.X, r.X) && q.X >= min(p.X, r.X) && q.Y <= max(p.Y, r.Y) && q.Y >= min(p.Y, r.Y)
}

// check if a polygon diagonal intersects any polygon segments
func intersectsPolygon[T float](a, b *node[T]) bool {
	// an edge whose bbox can't overlap the diagonal's bbox can't intersect it
	minX, maxX := min(a.X, b.X), max(a.X, b.X)
	minY, maxY := min(a.Y, b.Y), max(a.Y, b.Y)

	p := a
	for {
		n := p.Next

		outside := (p.X > maxX && n.X > maxX) || (p.X < minX && n.X < minX) ||
			(p.Y > maxY && n.Y > maxY) || (p.Y < minY && n.Y < minY)

		if !outside && p.Index != a.Index && n.Index != a.Index && p.Index != b.Index && n.Index != b.Index &&
			intersects(p, n, a, b, true) {
			return true
		}

		p = n
		if p == a {
			return false
		}
	}
}

// check if a polygon diagonal is locally inside the polygon
func locallyInside[T float](a, b *node[T]) bool {
	if area(a.Prev, a, a.Next) < 0 {
		return area(a, b, a.Next) >= 0 && area(a, a.Prev, b) >= 0
	}

	return area(a, b, a.Prev) < 0 || area(a, a.Next, b) < 0
}

// check if the middle point of a polygon diagonal is inside the polygon
func middleInside[T float](a, b *node[T]) bool {
	inside := false

	px := (a.X + b.X) / 2
	py := (a.Y + b.Y) / 2

	p := a
	for {
		n := p.Next

		// do not remove the extra T casts, they prevent go from emitting fma instructions
		//goland:noinspection GoRedundantConversion
		if ((p.Y > py) != (n.Y > py)) && (px < T((n.X-p.X)*(py-p.Y)/(n.Y-p.Y))+p.X) {
			inside = !inside
		}

		p = n
		if p == a {
			return inside
		}
	}
}

// link two polygon vertices with a bridge; if the vertices belong to the same ring, it splits polygon into two;
// if one belongs to the outer ring and another to a hole, it merges it into a single ring
func (e *earcut[T]) splitPolygon(a, b *node[T]) *node[T] {
	a2 := e.createNode(a.Index, a.Point)
	b2 := e.createNode(b.Index, b.Point)

	an := a.Next
	bp := b.Prev

	a.Next = b
	b.Prev = a

	a2.Next = an
	an.Prev = a2

	b2.Next = a2
	a2.Prev = b2

	bp.Next = b2
	b2.Prev = bp

	return b2
}

// create a node and optionally link it with previous one (in a circular doubly linked list)
func (e *earcut[T]) insertNode(idx uint32, point Point[T], last *node[T]) *node[T] {
	p := e.createNode(idx, point)

	if last == nil {
		p.Prev = p
		p.Next = p
	} else {
		p.Next = last.Next
		p.Prev = last
		last.Next.Prev = p
		last.Next = p
	}

	return p
}

func (e *earcut[T]) removeNode(p *node[T]) {
	p.Next.Prev = p.Prev
	p.Prev.Next = p.Next

	if p.PrevZ != nil {
		p.PrevZ.NextZ = p.NextZ
	}

	if p.NextZ != nil {
		p.NextZ.PrevZ = p.PrevZ
	}

	// keep the bboxes of the hole bridge index covering the healed prev->next edge
	if e.indexActive {
		e.growBlock(p.Prev, p.Next)
	}
}

func (e *earcut[T]) createNode(idx uint32, point Point[T]) *node[T] {
	n := e.alloc.Alloc()
	*n = node[T]{Point: point, Index: idx}
	return n
}

func signedArea[T float](points []Point[T]) float64 {
	var sum float64

	for i, j := 0, len(points)-1; i < len(points); j, i = i, i+1 {
		pi, pj := points[i], points[j]

		// do not remove the extra float64 casts, they prevent go from emitting fma instructions
		//goland:noinspection GoRedundantConversion
		sum += float64((float64(pj.X) - float64(pi.X)) * (float64(pi.Y) + float64(pj.Y)))
	}

	return sum
}

type node[T float] struct {
	Point[T]
	Prev, Next   *node[T]
	PrevZ, NextZ *node[T]

	// z-order curve value. During eliminateHoles it holds the index of the owning block.
	Z       int32
	Index   uint32
	Steiner bool
}

func (n *node[T]) EqualTo(other *node[T]) bool {
	return n.Point == other.Point
}

// alive reports whether the node is still linked into its ring
func (n *node[T]) alive() bool {
	return n.Prev.Next == n
}

// nodeAllocator hands out nodes from a buffer that is reused across calls.
// Nodes it returns are not zeroed.
type nodeAllocator[T float] struct {
	Expected int

	// main holds room for the expected number of nodes, buf is what is left of it
	main, buf []node[T]

	// extra chunks for nodes beyond the expected number (splits add two each), used counts the ones in use
	extra [][]node[T]
	used  int
}

func (a *nodeAllocator[T]) reset(expected int) {
	if cap(a.main) < expected {
		a.main = make([]node[T], expected)
	}

	a.Expected = expected
	a.buf = a.main[:expected]
	a.used = 0
}

func (a *nodeAllocator[T]) Alloc() *node[T] {
	if len(a.buf) == 0 {
		// more nodes than expected were requested, take a small extra chunk.
		// Nodes are referenced by pointer, so main cannot grow in place.
		if a.used == len(a.extra) {
			a.extra = append(a.extra, make([]node[T], 1024))
		}

		a.buf = a.extra[a.used]
		a.used++
	}

	n := &a.buf[0]
	a.buf = a.buf[1:]

	return n
}
