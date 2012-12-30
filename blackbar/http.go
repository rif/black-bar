package blackbar

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // import so we can read PNG files.
	"io"
	"log"
	"net/http"
	"strconv"
	"text/template"
)

// These imports were added for deployment on App Engine.
import (
	"appengine"
	"appengine/datastore"
	"crypto/sha1"
	"resize"
)

var (
	templates = template.Must(template.ParseFiles(
		"edit.html",
		"error.html",
		"upload.html",
	))
)

// Because App Engine owns main and starts the HTTP service,
// we do our setup during initialization.
func init() {
	http.HandleFunc("/", errorHandler(upload))
	http.HandleFunc("/edit", errorHandler(edit))
	http.HandleFunc("/img", errorHandler(img))
}

// Image is the type used to hold the image in the datastore.
type Image struct {
	Data []byte
}

// upload is the HTTP handler for uploading images; it handles "/".
func upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		// No upload; show the upload form.
		templates.ExecuteTemplate(w, "upload.html", nil)
		return
	}

	f, _, err := r.FormFile("image")
	check(err)
	defer f.Close()

	// Grab the image data
	var buf bytes.Buffer
	io.Copy(&buf, f)
	i, _, err := image.Decode(&buf)
	check(err)

	// Resize if too large, for more efficient blackbarring.
	// We aim for less than 1200 pixels in any dimension; if the
	// picture is larger than that, we squeeze it down to 600.
	const max = 1200
	if b := i.Bounds(); b.Dx() > max || b.Dy() > max {
		// If it's gigantic, it's more efficient to downsample first
		// and then resize; resizing will smooth out the roughness.
		if b.Dx() > 2*max || b.Dy() > 2*max {
			w, h := max, max
			if b.Dx() > b.Dy() {
				h = b.Dy() * h / b.Dx()
			} else {
				w = b.Dx() * w / b.Dy()
			}
			i = resize.Resample(i, i.Bounds(), w, h)
			b = i.Bounds()
		}
		w, h := max/2, max/2
		if b.Dx() > b.Dy() {
			h = b.Dy() * h / b.Dx()
		} else {
			w = b.Dx() * w / b.Dy()
		}
		i = resize.Resize(i, i.Bounds(), w, h)
	}

	// Encode as a new JPEG image.
	buf.Reset()
	err = jpeg.Encode(&buf, i, nil)
	check(err)

	// Create an App Engine context for the client's request.
	c := appengine.NewContext(r)

	// Save the image under a unique key, a hash of the image.
	key := datastore.NewKey(c, "Image", keyOf(buf.Bytes()), 0, nil)
	_, err = datastore.Put(c, key, &Image{buf.Bytes()})
	check(err)

	// Redirect to /edit using the key.
	http.Redirect(w, r, "/edit?id="+key.StringID(), http.StatusFound)
}

// keyOf returns (part of) the SHA-1 hash of the data, as a hex string.
func keyOf(data []byte) string {
	sha := sha1.New()
	sha.Write(data)
	return fmt.Sprintf("%x", string(sha.Sum(nil))[0:8])
}

// edit is the HTTP handler for editing images; it handles "/edit".
func edit(w http.ResponseWriter, r *http.Request) {
	templates.ExecuteTemplate(w, "edit.html", r.FormValue("id"))
}

// img is the HTTP handler for displaying images and painting blackbars;
// it handles "/img".
func img(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	key := datastore.NewKey(c, "Image", r.FormValue("id"), 0, nil)
	im := new(Image)
	err := datastore.Get(c, key, im)
	check(err)

	m, _, err := image.Decode(bytes.NewBuffer(im.Data))
	check(err)

	get := func(n string) int { // helper closure
		i, _ := strconv.Atoi(r.FormValue(n))
		return i
	}
	x, y, s := get("x"), get("y"), get("s")
	dst := blackbar(m, x, y, s)
	var buf bytes.Buffer
	jpeg.Encode(&buf, dst, nil)
	if r.FormValue("n") != "" { // save the current blackbar to store
		_, err = datastore.Put(c, key, &Image{buf.Bytes()})
		check(err)
	}
	w.Header().Set("Content-type", "image/jpeg")
	io.Copy(w, &buf)
}

func blackbar(m image.Image, x, y, s int) image.Image {
	dp := image.Pt(x, y)
	sr := image.Rect(0, 0, (s+1)*50, (s+1)*10)
	bbar := image.NewRGBA(sr)
	draw.Draw(bbar, bbar.Bounds(), image.NewUniform(color.Black), image.ZP, draw.Src)
	dst := rgba(m)
	dst.Set(x, y, color.Black)
	if x > 0 { // only draw if coordinates provided
		r := image.Rectangle{dp.Sub(sr.Size().Div(2)), dp.Add(sr.Size().Div(2))}
		draw.Draw(dst, r, bbar, image.ZP, draw.Src)
	}
	return dst
}

// rgba returns an RGBA version of the image, making a copy only if
// necessary.
func rgba(m image.Image) *image.RGBA {
	if r, ok := m.(*image.RGBA); ok {
		return r
	}
	b := m.Bounds()
	r := image.NewRGBA(b)
	draw.Draw(r, b, m, image.ZP, draw.Src)
	return r
}

// errorHandler wraps the argument handler with an error-catcher that
// returns a 500 HTTP error if the request fails (calls check with err non-nil).
func errorHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err, ok := recover().(error); ok {
				w.WriteHeader(http.StatusInternalServerError)
				templates.ExecuteTemplate(w, "error.html", err)
			}
		}()
		fn(w, r)
	}
}

// check aborts the current execution if err is non-nil.
func check(err error) {
	if err != nil {
		log.Print("Error: ", err)
		panic(err)
	}
}
