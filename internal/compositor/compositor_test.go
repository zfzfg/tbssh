package compositor

import "testing"

func solid(w, h int, r, g, b, a byte) []byte {
	px := make([]byte, w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = r, g, b, a
	}
	return px
}

func pixel(c *Compositor, x, y int) (byte, byte, byte, bool) {
	i := y*c.W + x
	return c.Canvas[i*4], c.Canvas[i*4+1], c.Canvas[i*4+2], c.Covered[i]
}

func TestPlacementWithOffsetsAndZ(t *testing.T) {
	c := New(8, 4, 10, 20) // 80x80 canvas
	c.Transmit(1, 0, 80, 80, solid(80, 80, 255, 255, 255, 255))
	c.Place(Placement{Image: 1, ID: 1})
	c.Transmit(2, 0, 5, 5, solid(5, 5, 255, 0, 0, 255))
	c.Place(Placement{Image: 2, ID: 1, Col: 2, Row: 1, OffX: 3, OffY: 4, Z: 2})
	c.Render()
	if r, g, _, cov := pixel(c, 23, 24); r != 255 || g != 0 || !cov {
		t.Fatalf("patch not drawn at offset: %d %d %v", r, g, cov)
	}
	if r, g, _, _ := pixel(c, 22, 24); r != 255 || g != 255 {
		t.Fatal("base not drawn next to patch")
	}
	// Replacing placement 1 of image 2 moves it.
	c.Place(Placement{Image: 2, ID: 1, Col: 0, Row: 0, Z: 2})
	c.Render()
	if _, g, _, _ := pixel(c, 23, 24); g != 255 {
		t.Fatal("old patch position not restored")
	}
	if _, g, _, _ := pixel(c, 1, 1); g != 0 {
		t.Fatal("patch not at new position")
	}
}

func TestFrameEditDamagesOnlyItsArea(t *testing.T) {
	c := New(4, 2, 10, 20)
	c.Transmit(1, 0, 40, 40, solid(40, 40, 0, 0, 0, 255))
	c.Place(Placement{Image: 1, ID: 1})
	c.Render()
	c.EditFrame(1, 10, 10, 2, 2, solid(2, 2, 9, 9, 9, 255), true)
	rects := c.Render()
	if len(rects) != 1 || rects[0] != (Rect{10, 10, 2, 2}) {
		t.Fatalf("damage %v", rects)
	}
	if r, _, _, _ := pixel(c, 11, 11); r != 9 {
		t.Fatal("frame edit not applied")
	}
}

func TestDeleteVariants(t *testing.T) {
	c := New(4, 2, 10, 20)
	c.Transmit(1, 0, 10, 10, solid(10, 10, 1, 1, 1, 255))
	c.Transmit(2, 0, 10, 10, solid(10, 10, 2, 2, 2, 255))
	c.Place(Placement{Image: 1, ID: 1})
	c.Place(Placement{Image: 2, ID: 1, Col: 2, Z: 5})
	c.Delete(DeleteSpec{What: 'z', Z: 5})
	if c.Last().Image != 1 || len(c.placements) != 1 {
		t.Fatal("delete by z failed")
	}
	if c.Image(2) == nil {
		t.Fatal("lowercase delete must keep image data")
	}
	c.Delete(DeleteSpec{What: 'I', ImageID: 1})
	if c.HasPlacements() || c.Image(1) != nil {
		t.Fatal("uppercase delete by id must free data")
	}
	c.Delete(DeleteSpec{What: 'I', ImageID: 2})
	if c.Image(2) != nil {
		t.Fatal("unplaced image not freed")
	}
	c.Render()
	if _, _, _, cov := pixel(c, 1, 1); cov {
		t.Fatal("deleted placement still covers the canvas")
	}
}

func TestTranslucentBlendsOverBackground(t *testing.T) {
	c := New(1, 1, 10, 20)
	c.SetBackground(0, 0, 200)
	c.Transmit(1, 0, 1, 1, []byte{255, 0, 0, 128})
	c.Place(Placement{Image: 1})
	c.Render()
	r, _, b, cov := pixel(c, 0, 0)
	if !cov || r < 120 || r > 135 || b < 95 || b > 105 {
		t.Fatalf("blend got r=%d b=%d", r, b)
	}
}

func TestScaledPlacement(t *testing.T) {
	c := New(4, 2, 10, 20)
	px := solid(2, 2, 0, 0, 0, 255)
	copy(px[4:8], []byte{255, 255, 255, 255}) // top-right pixel white
	c.Transmit(1, 0, 2, 2, px)
	c.Place(Placement{Image: 1, Cols: 2, Rows: 1})
	c.Render()
	if p := c.Last().Dest(); p != (Rect{0, 0, 20, 20}) {
		t.Fatalf("dest %v", p)
	}
	if r, _, _, _ := pixel(c, 15, 5); r != 255 {
		t.Fatal("scaled top-right not white")
	}
	if r, _, _, _ := pixel(c, 5, 15); r != 0 {
		t.Fatal("scaled bottom-left not black")
	}
}
