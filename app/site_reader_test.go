package app

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/url"
	"testing"
)

func TestSdlibCNReaderBookID(t *testing.T) {
	r := NewSdlib()
	r.parsedUrl, _ = url.Parse("https://guji.sdlib.cn/#/reader/150113020240030?treeId=1972855219116380162")
	if got := r.getBookId(r.parsedUrl.String()); got != "150113020240030" {
		t.Fatalf("book id = %q", got)
	}
	if !r.isCNReader() {
		t.Fatal(".cn reader was not detected")
	}
}

func TestTianyigeBase58AndSliceLayout(t *testing.T) {
	const input = "1.489035d42ac94565bc673bd84eda07a2.1793787248238"
	const want = "2odsFYBsTJMZzsAxAz8fqFrQPktWW4sX3i1GDXJYsNvf4MSPHtiX5GPkztZEBPJuiP"
	if got := tianyigeBase58(input); got != want {
		t.Fatalf("base58 = %q, want %q", got, want)
	}
	if got := tianyigeSliceCount("20171e80c1cf429f83ce9de4e4ccda97"); got != 12 {
		t.Fatalf("slice count = %d, want 12", got)
	}
	if got := tianyigeSliceCount("989035d42ac94565bc673bd84eda07a2"); got != 8 {
		t.Fatalf("slice count = %d, want 8", got)
	}

	// Six slices use a 3-column layout.  The decoded output must contain the
	// complete 3x2 grid rather than one physical slice.
	var slices [][]byte
	for i := 0; i < 6; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 2, 3))
		for y := 0; y < 3; y++ {
			for x := 0; x < 2; x++ {
				img.Set(x, y, color.RGBA{uint8(i * 30), uint8(y * 30), 0, 255})
			}
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, nil); err != nil {
			t.Fatal(err)
		}
		slices = append(slices, b.Bytes())
	}
	out, err := stitchTianyigeSlices(slices, 6)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Bounds().Size(); got.X != 6 || got.Y != 6 {
		t.Fatalf("stitched size = %v, want (6,6)", got)
	}
}
