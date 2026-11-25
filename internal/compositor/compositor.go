// Package compositor keeps the images and placements a kitty graphics client
// created and composes them into one screen-sized RGBA canvas, tracking which
// parts of it changed.
package compositor

import (
	"sort"
)

// Rect is a pixel rectangle.
type Rect struct{ X, Y, W, H int }

// Empty reports whether r has no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Intersect returns the overlap of r and o.
func (r Rect) Intersect(o Rect) Rect {
	x0, y0 := max(r.X, o.X), max(r.Y, o.Y)
	x1, y1 := min(r.X+r.W, o.X+o.W), min(r.Y+r.H, o.Y+o.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Union returns the bounding box of r and o.
func (r Rect) Union(o Rect) Rect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	x0, y0 := min(r.X, o.X), min(r.Y, o.Y)
	x1, y1 := max(r.X+r.W, o.X+o.W), max(r.Y+r.H, o.Y+o.H)
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Image is transmitted pixel data, straight (not premultiplied) RGBA.
type Image struct {
	W, H   int
	Pix    []byte
	Number int
	seq    uint64
}

// Placement shows (part of) an image on the screen.
type Placement struct {
	Image uint32 // image id
	ID    uint32 // placement id, 0 when the client gave none

	Col, Row   int  // anchor cell, 0-based
	OffX, OffY int  // pixel offset inside the anchor cell
	Src        Rect // source rectangle; zero W/H means "to the image edge"
	Cols, Rows int  // display size in cells, 0 = natural size
	Z          int

	seq  uint64
	dest Rect
	src  Rect
}

// Dest returns where the placement is drawn, in canvas pixels.
func (p *Placement) Dest() Rect { return p.dest }

// Compositor holds images and placements and the composed canvas.
type Compositor struct {
	CW, CH     int // cell size in pixels
	Cols, Rows int
	W, H       int

	// Canvas holds the composed pixels, opaque RGBA. Only pixels whose
	// Covered entry is true belong to an image.
	Canvas  []byte
	Covered []bool

	images     map[uint32]*Image
	numbers    map[int]uint32
	placements []*Placement
	seq        uint64
	damage     []Rect
	imageBytes int
	nextAuto   uint32

	bg [3]byte
}

// MaxImageBytes bounds the memory held by transmitted images.
const MaxImageBytes = 768 << 20

// New makes a compositor for a cols x rows grid of cw x ch pixel cells.
func New(cols, rows, cw, ch int) *Compositor {
	c := &Compositor{
		CW: cw, CH: ch,
		images:   make(map[uint32]*Image),
		numbers:  make(map[int]uint32),
		nextAuto: 1 << 31,
	}
	c.Resize(cols, rows)
	return c
}

// SetBackground sets the color translucent pixels are blended over when no
// other image is below them.
func (c *Compositor) SetBackground(r, g, b byte) { c.bg = [3]byte{r, g, b} }

// Resize changes the grid. Placements are kept; everything is redrawn.
func (c *Compositor) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	c.Cols, c.Rows = cols, rows
	c.W, c.H = cols*c.CW, rows*c.CH
	c.Canvas = make([]byte, c.W*c.H*4)
	c.Covered = make([]bool, c.W*c.H)
	for _, p := range c.placements {
		c.layout(p)
	}
	c.DamageAll()
}

// DamageAll marks the whole canvas changed.
func (c *Compositor) DamageAll() {
	c.damage = append(c.damage[:0], Rect{0, 0, c.W, c.H})
}

// Damage marks a canvas rectangle changed.
func (c *Compositor) Damage(r Rect) {
	r = r.Intersect(Rect{0, 0, c.W, c.H})
	if r.Empty() {
		return
	}
	c.damage = append(c.damage, r)
	if len(c.damage) > 128 {
		u := Rect{}
		for _, d := range c.damage {
			u = u.Union(d)
		}
		c.damage = append(c.damage[:0], u)
	}
}

// DamageRows marks cell rows r0..r1 (inclusive) changed.
func (c *Compositor) DamageRows(r0, r1 int) {
	c.Damage(Rect{0, r0 * c.CH, c.W, (r1 - r0 + 1) * c.CH})
}

// Dirty reports whether Render has anything to do.
func (c *Compositor) Dirty() bool { return len(c.damage) > 0 }

// HasPlacements reports whether anything is shown.
func (c *Compositor) HasPlacements() bool { return len(c.placements) > 0 }

// CoversRows reports whether any placement overlaps cell rows r0..r1.
func (c *Compositor) CoversRows(r0, r1 int) bool {
	band := Rect{0, r0 * c.CH, c.W, (r1 - r0 + 1) * c.CH}
	for _, p := range c.placements {
		if !p.dest.Intersect(band).Empty() {
			return true
		}
	}
	return false
}

// Image returns the image with the given id.
func (c *Compositor) Image(id uint32) *Image { return c.images[id] }

// ImageByNumber resolves an image number (I=) to its id.
func (c *Compositor) ImageByNumber(n int) (uint32, bool) {
	id, ok := c.numbers[n]
	return id, ok
}

// AllocID returns a fresh image id for a transmission that only gave I=.
func (c *Compositor) AllocID() uint32 {
	for {
		c.nextAuto++
		if c.nextAuto == 0 {
			c.nextAuto = 1 << 31
		}
		if _, used := c.images[c.nextAuto]; !used {
			return c.nextAuto
		}
	}
}

// Transmit stores image data under id, replacing older data.
func (c *Compositor) Transmit(id uint32, number int, w, h int, pix []byte) {
	if old, ok := c.images[id]; ok {
		c.imageBytes -= len(old.Pix)
	}
	own := make([]byte, len(pix))
	copy(own, pix)
	c.seq++
	c.images[id] = &Image{W: w, H: h, Pix: own, Number: number, seq: c.seq}
	if number != 0 {
		c.numbers[number] = id
	}
	c.imageBytes += len(own)
	for _, p := range c.placements {
		if p.Image == id {
			c.Damage(p.dest)
			c.layout(p)
			c.Damage(p.dest)
		}
	}
	c.evict()
}

func (c *Compositor) evict() {
	if c.imageBytes <= MaxImageBytes {
		return
	}
	placed := map[uint32]bool{}
	for _, p := range c.placements {
		placed[p.Image] = true
	}
	type entry struct {
		id  uint32
		seq uint64
	}
	var unplaced []entry
	for id, img := range c.images {
		if !placed[id] {
			unplaced = append(unplaced, entry{id, img.seq})
		}
	}
	sort.Slice(unplaced, func(i, j int) bool { return unplaced[i].seq < unplaced[j].seq })
	for _, e := range unplaced {
		if c.imageBytes <= MaxImageBytes {
			return
		}
		c.freeImage(e.id)
	}
}

func (c *Compositor) freeImage(id uint32) {
	img, ok := c.images[id]
	if !ok {
		return
	}
	c.imageBytes -= len(img.Pix)
	if img.Number != 0 && c.numbers[img.Number] == id {
		delete(c.numbers, img.Number)
	}
	delete(c.images, id)
}

// EditFrame overwrites (replace=true) or blends a rectangle of an image's
// pixels, like kitty's a=f on the root frame.
func (c *Compositor) EditFrame(id uint32, x, y, w, h int, pix []byte, replace bool) bool {
	img, ok := c.images[id]
	if !ok {
		return false
	}
	r := Rect{x, y, w, h}.Intersect(Rect{0, 0, img.W, img.H})
	if r.Empty() {
		return true
	}
	for row := 0; row < r.H; row++ {
		sy := r.Y - y + row
		sx := r.X - x
		src := pix[(sy*w+sx)*4 : (sy*w+sx+r.W)*4]
		dst := img.Pix[((r.Y+row)*img.W+r.X)*4 : ((r.Y+row)*img.W+r.X+r.W)*4]
		if replace {
			copy(dst, src)
			continue
		}
		for i := 0; i < len(src); i += 4 {
			blendStraight(dst[i:i+4], src[i:i+4])
		}
	}
	for _, p := range c.placements {
		if p.Image != id {
			continue
		}
		c.Damage(c.mapSourceRect(p, r))
	}
	return true
}

func blendStraight(dst, src []byte) {
	sa := int(src[3])
	if sa == 255 {
		copy(dst, src)
		return
	}
	if sa == 0 {
		return
	}
	da := int(dst[3])
	oa := sa + da*(255-sa)/255
	if oa == 0 {
		dst[0], dst[1], dst[2], dst[3] = 0, 0, 0, 0
		return
	}
	for k := 0; k < 3; k++ {
		v := (int(src[k])*sa + int(dst[k])*da*(255-sa)/255) / oa
		dst[k] = byte(v)
	}
	dst[3] = byte(oa)
}

// mapSourceRect maps an image-space rectangle to the canvas for placement p.
func (c *Compositor) mapSourceRect(p *Placement, r Rect) Rect {
	r = r.Intersect(p.src)
	if r.Empty() || p.src.W == 0 || p.src.H == 0 {
		return Rect{}
	}
	x0 := p.dest.X + (r.X-p.src.X)*p.dest.W/p.src.W
	y0 := p.dest.Y + (r.Y-p.src.Y)*p.dest.H/p.src.H
	x1 := p.dest.X + ((r.X+r.W-p.src.X)*p.dest.W+p.src.W-1)/p.src.W
	y1 := p.dest.Y + ((r.Y+r.H-p.src.Y)*p.dest.H+p.src.H-1)/p.src.H
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Place adds or replaces a placement. It returns false when the image is
// unknown.
func (c *Compositor) Place(p Placement) bool {
	if _, ok := c.images[p.Image]; !ok {
		return false
	}
	if p.ID != 0 {
		for i, old := range c.placements {
			if old.Image == p.Image && old.ID == p.ID {
				c.Damage(old.dest)
				c.placements = append(c.placements[:i], c.placements[i+1:]...)
				break
			}
		}
	}
	c.seq++
	np := p
	np.seq = c.seq
	c.layout(&np)
	c.placements = append(c.placements, &np)
	c.Damage(np.dest)
	return true
}

func (c *Compositor) layout(p *Placement) {
	img := c.images[p.Image]
	if img == nil {
		p.dest, p.src = Rect{}, Rect{}
		return
	}
	src := p.Src
	if src.W <= 0 {
		src.W = img.W - src.X
	}
	if src.H <= 0 {
		src.H = img.H - src.Y
	}
	src = src.Intersect(Rect{0, 0, img.W, img.H})
	p.src = src
	w, h := src.W, src.H
	switch {
	case p.Cols > 0 && p.Rows > 0:
		w, h = p.Cols*c.CW, p.Rows*c.CH
	case p.Cols > 0 && src.W > 0:
		w = p.Cols * c.CW
		h = src.H * w / src.W
	case p.Rows > 0 && src.H > 0:
		h = p.Rows * c.CH
		w = src.W * h / src.H
	}
	p.dest = Rect{p.Col*c.CW + p.OffX, p.Row*c.CH + p.OffY, w, h}
}

// CellSpan returns how many columns and rows a placement covers, counted
// from its anchor cell.
func (c *Compositor) CellSpan(p *Placement) (cols, rows int) {
	cols = (p.OffX + p.dest.W + c.CW - 1) / c.CW
	rows = (p.OffY + p.dest.H + c.CH - 1) / c.CH
	return max(cols, 1), max(rows, 1)
}

// Last returns the most recently added placement.
func (c *Compositor) Last() *Placement {
	if len(c.placements) == 0 {
		return nil
	}
	return c.placements[len(c.placements)-1]
}

// DeleteSpec selects what a=d removes.
type DeleteSpec struct {
	What      byte // the d= key; uppercase also frees image data
	ImageID   uint32
	Placement uint32
	Number    int
	X, Y      int // 1-based cell or id range, as in the protocol
	Z         int
	CursorCol int
	CursorRow int
}

// Delete removes placements (and for uppercase specs, image data).
func (c *Compositor) Delete(s DeleteSpec) {
	what := s.What
	if what == 0 {
		what = 'a'
	}
	free := what >= 'A' && what <= 'Z'
	lower := what | 0x20

	cellHit := func(p *Placement, col, row int) bool {
		cell := Rect{col * c.CW, row * c.CH, c.CW, c.CH}
		return !p.dest.Intersect(cell).Empty()
	}
	var match func(p *Placement) bool
	switch lower {
	case 'a':
		match = func(p *Placement) bool { return true }
	case 'i':
		match = func(p *Placement) bool {
			return p.Image == s.ImageID && (s.Placement == 0 || p.ID == s.Placement)
		}
	case 'n':
		id, ok := c.numbers[s.Number]
		if !ok {
			return
		}
		s.ImageID = id
		match = func(p *Placement) bool {
			return p.Image == id && (s.Placement == 0 || p.ID == s.Placement)
		}
	case 'c':
		match = func(p *Placement) bool { return cellHit(p, s.CursorCol, s.CursorRow) }
	case 'p':
		match = func(p *Placement) bool { return cellHit(p, s.X-1, s.Y-1) }
	case 'q':
		match = func(p *Placement) bool { return cellHit(p, s.X-1, s.Y-1) && p.Z == s.Z }
	case 'x':
		col := Rect{(s.X - 1) * c.CW, 0, c.CW, c.H}
		match = func(p *Placement) bool { return !p.dest.Intersect(col).Empty() }
	case 'y':
		row := Rect{0, (s.Y - 1) * c.CH, c.W, c.CH}
		match = func(p *Placement) bool { return !p.dest.Intersect(row).Empty() }
	case 'z':
		match = func(p *Placement) bool { return p.Z == s.Z }
	case 'r':
		match = func(p *Placement) bool { return int(p.Image) >= s.X && int(p.Image) <= s.Y }
	default:
		return
	}

	affected := map[uint32]bool{}
	kept := c.placements[:0]
	for _, p := range c.placements {
		if match(p) {
			c.Damage(p.dest)
			affected[p.Image] = true
			continue
		}
		kept = append(kept, p)
	}
	for i := len(kept); i < len(c.placements); i++ {
		c.placements[i] = nil
	}
	c.placements = kept

	if !free {
		return
	}
	if lower == 'i' || lower == 'n' {
		affected[s.ImageID] = true
	}
	if lower == 'r' {
		for id := range c.images {
			if int(id) >= s.X && int(id) <= s.Y {
				affected[id] = true
			}
		}
	}
	for id := range affected {
		still := false
		for _, p := range c.placements {
			if p.Image == id {
				still = true
				break
			}
		}
		if !still {
			c.freeImage(id)
		}
	}
}

// ClearPlacements removes every placement but keeps image data, like a
// terminal does when the screen is cleared.
func (c *Compositor) ClearPlacements() {
	if len(c.placements) > 0 {
		c.DamageAll()
	}
	c.placements = nil
}

// Reset forgets everything.
func (c *Compositor) Reset() {
	c.ClearPlacements()
	c.images = make(map[uint32]*Image)
	c.numbers = make(map[int]uint32)
	c.imageBytes = 0
	c.DamageAll()
}

// Scroll moves placements anchored in rows top..bottom up by n rows (down
// for negative n), dropping those that leave the region.
func (c *Compositor) Scroll(n, top, bottom int) {
	if n == 0 || len(c.placements) == 0 {
		return
	}
	kept := c.placements[:0]
	for _, p := range c.placements {
		if p.Row >= top && p.Row <= bottom {
			p.Row -= n
			_, rows := c.CellSpan(p)
			if p.Row+rows-1 < top || p.Row > bottom {
				continue
			}
			c.layout(p)
		}
		kept = append(kept, p)
	}
	c.placements = kept
	c.DamageRows(top, bottom)
}

// Render recomposes every damaged area and returns the damaged rectangles.
func (c *Compositor) Render() []Rect {
	if len(c.damage) == 0 {
		return nil
	}
	rects := mergeRects(c.damage)
	c.damage = c.damage[:0]

	order := make([]*Placement, len(c.placements))
	copy(order, c.placements)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Z != order[j].Z {
			return order[i].Z < order[j].Z
		}
		return order[i].seq < order[j].seq
	})

	for _, r := range rects {
		for y := r.Y; y < r.Y+r.H; y++ {
			row := y * c.W
			clear(c.Covered[row+r.X : row+r.X+r.W])
		}
		for _, p := range order {
			area := p.dest.Intersect(r)
			if area.Empty() {
				continue
			}
			c.blit(p, area)
		}
	}
	return rects
}

func (c *Compositor) blit(p *Placement, area Rect) {
	img := c.images[p.Image]
	if img == nil || p.src.Empty() || p.dest.Empty() {
		return
	}
	scaled := p.dest.W != p.src.W || p.dest.H != p.src.H
	for y := area.Y; y < area.Y+area.H; y++ {
		sy := p.src.Y + (y-p.dest.Y)*p.src.H/p.dest.H
		if !scaled {
			sy = p.src.Y + (y - p.dest.Y)
		}
		srow := sy * img.W
		drow := y * c.W
		for x := area.X; x < area.X+area.W; x++ {
			var sx int
			if scaled {
				sx = p.src.X + (x-p.dest.X)*p.src.W/p.dest.W
			} else {
				sx = p.src.X + (x - p.dest.X)
			}
			s := img.Pix[(srow+sx)*4 : (srow+sx)*4+4]
			a := s[3]
			if a == 0 {
				continue
			}
			di := drow + x
			d := c.Canvas[di*4 : di*4+4]
			if a == 255 {
				d[0], d[1], d[2], d[3] = s[0], s[1], s[2], 255
				c.Covered[di] = true
				continue
			}
			var under [3]byte
			if c.Covered[di] {
				under = [3]byte{d[0], d[1], d[2]}
			} else {
				under = c.bg
			}
			ia := 255 - int(a)
			d[0] = byte((int(s[0])*int(a) + int(under[0])*ia) / 255)
			d[1] = byte((int(s[1])*int(a) + int(under[1])*ia) / 255)
			d[2] = byte((int(s[2])*int(a) + int(under[2])*ia) / 255)
			d[3] = 255
			c.Covered[di] = true
		}
	}
}

// mergeRects joins overlapping rectangles so no pixel is composed twice.
func mergeRects(in []Rect) []Rect {
	out := make([]Rect, 0, len(in))
	for _, r := range in {
		if !r.Empty() {
			out = append(out, r)
		}
	}
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if !out[i].Intersect(out[j]).Empty() {
					out[i] = out[i].Union(out[j])
					out = append(out[:j], out[j+1:]...)
					changed = true
					j--
				}
			}
		}
	}
	return out
}
