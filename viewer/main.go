package main

import (
	"encoding/json"
	"image/color"
	"os"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/oliverbestmann/byke/gm"
	"github.com/oliverbestmann/earcut-go"
)

var files = os.DirFS("../testdata")

func main() {
	testCase := "water-huge"

	fp, _ := files.Open(testCase + ".json")

	var data [][][2]float64
	_ = json.NewDecoder(fp).Decode(&data)

	outer := pointsOf(data[0])

	var holes [][]gm.Vec
	for _, hole := range data[1:] {
		holes = append(holes, pointsOf(hole))
	}

	minX, minY := outer[0].XY()
	maxX, maxY := outer[0].XY()

	for _, point := range outer {
		minX = min(minX, point.X)
		maxX = max(maxX, point.X)
		minY = min(minY, point.Y)
		maxY = max(maxY, point.Y)
	}

	var g ebiten.GeoM
	g.Translate(-minX, -minY)
	g.Scale(1/(maxX-minX), 1/(maxY-minY))

	// cast points into target format
	earcutOuter := castSlice[gm.Vec, earcut.Point[float64]](outer)

	var earcutHoles [][]earcut.Point[float64]
	for _, hole := range holes {
		earcutHoles = append(earcutHoles, castSlice[gm.Vec, earcut.Point[float64]](hole))
	}

	points, indices := earcut.Triangulate(earcutOuter, earcutHoles)

	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.RunGame(viewerGame{
		G:     g,
		Outer: outer,
		Holes: holes,

		Vertices: castSlice[earcut.Point[float64], gm.Vec](points),
		Indices:  indices,
	})
}

func castSlice[F, T any](points []F) []T {
	data := (*T)(unsafe.Pointer(unsafe.SliceData(points)))
	return unsafe.Slice(data, len(points))
}

type viewerGame struct {
	Outer []gm.Vec
	Holes [][]gm.Vec
	G     ebiten.GeoM

	Vertices []gm.Vec
	Indices  []uint32
}

func (v viewerGame) Update() error {
	return nil
}

func (v viewerGame) Draw(screen *ebiten.Image) {
	sw, sh := float64(screen.Bounds().Dx()), float64(screen.Bounds().Dy())

	toScreen := v.G
	toScreen.Scale(0.9*sw, 0.9*sh)
	toScreen.Translate(0.05*sw, 0.05*sh)

	// draw triangulation
	var triangles vector.Path
	for idx := 0; idx < len(v.Indices); idx += 3 {
		i0, i1, i2 := v.Indices[idx], v.Indices[idx+1], v.Indices[idx+2]
		v0, v1, v2 := v.Vertices[i0], v.Vertices[i1], v.Vertices[i2]

		x0, y0 := toScreen.Apply(v0.XY())
		x1, y1 := toScreen.Apply(v1.XY())
		x2, y2 := toScreen.Apply(v2.XY())

		triangles.MoveTo(float32(x0), float32(y0))
		triangles.LineTo(float32(x1), float32(y1))
		triangles.LineTo(float32(x2), float32(y2))
		triangles.LineTo(float32(x0), float32(y0))
	}

	dop := &vector.DrawPathOptions{}
	dop.ColorScale.ScaleWithColor(color.NRGBA{R: 128, G: 255, B: 128, A: 50})
	vector.FillPath(screen, &triangles, nil, dop)

	v.drawPolygon(screen, toScreen, v.Outer, color.White)

	for _, hole := range v.Holes {
		v.drawPolygon(screen, toScreen, hole, color.NRGBA{R: 255, G: 128, B: 0, A: 128})
	}

	dop = &vector.DrawPathOptions{}
	dop.ColorScale.ScaleWithColor(color.NRGBA{255, 255, 255, 128})
	vector.StrokePath(screen, &triangles, &vector.StrokeOptions{Width: 1}, dop)
}

func (v viewerGame) drawPolygon(screen *ebiten.Image, toScreen ebiten.GeoM, points []gm.Vec, color color.Color) {
	outer := v.pathOf(points, toScreen)

	dop := &vector.DrawPathOptions{}
	dop.ColorScale.ScaleWithColor(color)
	vector.StrokePath(screen, &outer, &vector.StrokeOptions{Width: 2}, dop)
}

func (v viewerGame) pathOf(points []gm.Vec, toScreen ebiten.GeoM) vector.Path {
	var outer vector.Path
	for _, point := range points {
		x, y := toScreen.Apply(point.X, point.Y)
		outer.LineTo(float32(x), float32(y))
	}

	outer.Close()
	return outer
}

func (v viewerGame) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return outsideWidth, outsideHeight
}

func dataToPoints(data [][][2]float64) ([]gm.Vec, [][]gm.Vec) {
	outer := pointsOf(data[0])

	var holes [][]gm.Vec
	for _, hole := range data[1:] {
		holes = append(holes, pointsOf(hole))
	}
	return outer, holes
}

func pointsOf(points [][2]float64) []gm.Vec {
	data := unsafe.SliceData(points)
	return unsafe.Slice((*gm.Vec)(unsafe.Pointer(data)), len(points))
}
