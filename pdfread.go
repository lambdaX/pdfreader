package pdfread

import (
  "io";
  "os";
  "regexp";
  "bytes";
  "compress/zlib";
  "fancy";
)

// limits

const MAX_PDF_UPDATES = 1024
const MAX_PDF_STRING = 1024
const MAX_PDF_DICT = 1024 * 16
const MAX_PDF_ARRAYSIZE = 1024

// types

type PdfReaderT struct {
  File      string;            // name of the file
  rdr       fancy.Reader;      // reader for the contents
  Startxref int;               // starting of xref table
  Xref      map[int]int;       // "pointers" of the xref table
  Trailer   map[string][]byte; // trailer dictionary of the file
  rcache    map[string][]byte; // resolver cache
  rncache   map[string]int;    // resolver cache (positions in file)
  pages     [][]byte;          // pages cache
}

var _Bytes = []byte{}

func max(a, b int) int {
  if a < b {
    return b
  }
  return a;
}
func min(a, b int) int {
  if a < b {
    return a
  }
  return b;
}
func end(a []byte, n int) int { return max(0, len(a)-n) }

func num(n []byte) (r int) {
  for i := 0; i < len(n); i++ {
    if n[i] >= '0' && n[i] <= '9' {
      r = r*10 + int(n[i]-'0')
    } else {
      break
    }
  }
  return;
}

func skipLE(f fancy.Reader) {
  for {
    c, err := f.ReadByte();
    if err != nil {
      return
    }
    if c > 32 {
      f.UnreadByte();
      return;
    }
    if c == 13 {
      c, err = f.ReadByte();
      if err == nil && c != 10 {
        f.UnreadByte()
      }
      return;
    }
    if c == 10 {
      return
    }
  }
}

func skipSpaces(f fancy.Reader) byte {
  for {
    c, err := f.ReadByte();
    if err != nil {
      break
    }
    if c > 32 {
      return c
    }
  }
  return 0;
}

func skipToDelim(f fancy.Reader) byte {
  for {
    c, err := f.ReadByte();
    if err != nil {
      break
    }
    if c < 33 {
      return c
    }
    switch c {
    case '<', '>', '(', ')', '[', ']', '/', '%':
      return c
    }
  }
  return 255;
}

func skipString(f fancy.Reader) {
  for depth := 1; depth > 0; {
    c, err := f.ReadByte();
    if err != nil {
      break
    }
    switch c {
    case '(':
      depth++
    case ')':
      depth--
    case '\\':
      f.ReadByte()
    }
  }
}

func skipComment(f fancy.Reader) {
  for {
    c, err := f.ReadByte();
    if err != nil || c == 13 || c == 10 {
      break
    }
  }
}

func skipComposite(f fancy.Reader) {
  for depth := 1; depth > 0; {
    switch skipToDelim(f) {
    case '<', '[':
      depth++
    case '>', ']':
      depth--
    case '(':
      skipString(f)
    case '%':
      skipComment(f)
    }
  }
}

func fpos(f fancy.Reader) int64 {
  r, _ := f.Seek(0, 1);
  return r;
}

func simpleToken(f fancy.Reader) ([]byte, int64) {
again:
  c := skipSpaces(f);
  if c == 0 {
    return _Bytes, -1
  }
  p := fpos(f) - 1;
  switch c {
  case '%':
    skipComment(f);
    goto again;
  case '<', '[':
    skipComposite(f)
  case '(':
    skipString(f)
  default:
    if skipToDelim(f) != 255 {
      f.UnreadByte()
    }
  }
  r := make([]byte, fpos(f)-p);
  f.ReadAt(r, p);
  return r, p;
}

func refToken(f fancy.Reader) ([]byte, int64) {
  tok, p := simpleToken(f);
  if len(tok) > 0 && tok[0] >= '0' && tok[0] <= '9' {
    simpleToken(f);
    r, q := simpleToken(f);
    if string(r) == "R" {
      tok = make([]byte, 1+q-p);
      f.ReadAt(tok, p);
    } else {
      f.Seek(p+int64(len(tok)), 0)
    }
  }
  return tok, p;
}

func tupel(f fancy.Reader, count int) [][]byte {
  r := make([][]byte, count);
  for i := 0; i < count; i++ {
    r[i], _ = simpleToken(f)
  }
  return r;
}

var xref = regexp.MustCompile(
  "startxref[\t ]*(\r?\n|\r)"
    "[\t ]*([0-9]+)[\t ]*(\r?\n|\r)"
    "[\t ]*%%EOF")

// xrefStart() queries the start of the xref-table in a PDF file.
func xrefStart(f fancy.Reader) int {
  s := int(f.Size());
  pdf := make([]byte, min(s, 1024));
  f.ReadAt(pdf, int64(max(0, s-1024)));
  ps := xref.AllMatches(pdf, 0);
  if ps == nil {
    return -1
  }
  return num(xref.MatchSlices(ps[len(ps)-1])[2]);
}

// xrefSkip() queries the start of the trailer for a (partial) xref-table.
func xrefSkip(f fancy.Reader, xref int) int {
  f.Seek(int64(xref), 0);
  t, p := simpleToken(f);
  if string(t) != "xref" {
    return -1
  }
  for {
    t, p = simpleToken(f);
    if t[0] < '0' || t[0] > '9' {
      f.Seek(p, 0);
      break;
    }
    t, _ = simpleToken(f);
    skipLE(f);
    f.Seek(int64(num(t)*20), 1);
  }
  return int(fpos(f));
}

// Dictionary() makes a map/hash from PDF dictionary data.
func Dictionary(s []byte) map[string][]byte {
  if len(s) < 4 {
    return nil
  }
  e := len(s) - 1;
  if s[0] != s[1] || s[0] != '<' || s[e] != s[e-1] || s[e] != '>' {
    return nil
  }
  r := make(map[string][]byte);
  rdr := fancy.SliceReader(s[2 : e-1]);
  for {
    t, _ := simpleToken(rdr);
    if len(t) == 0 {
      break
    }
    if t[0] != '/' {
      return nil
    }
    k := string(t);
    t, _ = refToken(rdr);
    r[k] = t;
  }
  return r;
}

// Array() extracts an array from PDF data.
func Array(s []byte) [][]byte {
  if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
    return nil
  }
  rdr := fancy.SliceReader(s[1 : len(s)-1]);
  r := make([][]byte, MAX_PDF_ARRAYSIZE);
  b := 0;
  for {
    r[b], _ = refToken(rdr);
    if len(r[b]) == 0 {
      break
    }
    b++;
  }
  if b == 0 {
    return nil
  }
  return r[0:b];
}

// xrefRead() reads the xref table(s) of a PDF file. This is not recursive
// in favour of not to have to keep track of already used starting points
// for xrefs.
func xrefRead(f fancy.Reader, p int) map[int]int {
  var back [MAX_PDF_UPDATES]int;
  b := 0;
  s := _Bytes;
  for ok := true; ok; {
    back[b] = p;
    b++;
    p = xrefSkip(f, p);
    f.Seek(int64(p), 0);
    s, _ = simpleToken(f);
    if string(s) != "trailer" {
      return nil
    }
    s, _ = simpleToken(f);
    s, ok = Dictionary(s)["/Prev"];
    p = num(s);
  }
  r := make(map[int]int);
  for b != 0 {
    b--;
    f.Seek(int64(back[b]), 0);
    simpleToken(f); // skip "xref"
    for {
      m := tupel(f, 2);
      if string(m[0]) == "trailer" {
        break
      }
      skipLE(f);
      o := num(m[0]);
      dat := f.Slice(num(m[1]) * 20);
      for i := 0; i < len(dat); i += 20 {
        if dat[i+17] != 'n' {
          r[o] = 0, false
        } else {
          r[o] = num(dat[i : i+10])
        }
        o++;
      }
    }
  }
  return r;
}

// object() extracts the top informations of a PDF "object". For streams
// this would be the dictionary as bytes.  It also returns the position in
// binary data where one has to continue to read for this "object".
func (pd *PdfReaderT) object(o int) (int, []byte) {
  p, ok := pd.Xref[o];
  if !ok {
    return -1, _Bytes
  }
  pd.rdr.Seek(int64(p), 0);
  m := tupel(pd.rdr, 3);
  if num(m[0]) != o {
    return -1, _Bytes
  }
  r, np := refToken(pd.rdr);
  return int(np) + len(r), r;
}

var res = regexp.MustCompile("^"
  "([0-9]+)"
  "[\r\n\t ]+"
  "[0-9]+"
  "[\r\n\t ]+"
  "R$")

// pd.Resolve() resolves a reference in the PDF file. You'll probably need
// this method for reading streams only.
func (pd *PdfReaderT) Resolve(s []byte) (int, []byte) {
  n := -1;
  if len(s) >= 5 && s[len(s)-1] == 'R' {
    z, ok := pd.rcache[string(s)];
    if ok {
      return pd.rncache[string(s)], z
    }
    done := make(map[int]int);
    orig := s;
  redo:
    m := res.MatchSlices(s);
    if m != nil {
      n = num(m[1]);
      if _, wrong := done[n]; wrong {
        return -1, _Bytes
      }
      done[n] = 1;
      n, s = pd.object(n);
      if z, ok = pd.rcache[string(s)]; !ok {
        goto redo
      }
      s = z;
      n = pd.rncache[string(s)];
    }
    pd.rcache[string(orig)] = s;
    pd.rncache[string(orig)] = n;
  }
  return n, s;
}

// pd.Obj() is the universal method to access contents of PDF objects or
// data tokens in i.e.  dictionaries.  For reading streams you'll have to
// utilize pd.Resolve().
func (pd *PdfReaderT) Obj(reference []byte) []byte {
  _, r := pd.Resolve(reference);
  return r;
}

// pd.Num() queries integer data from a reference.
func (pd *PdfReaderT) Num(reference []byte) int {
  return num(pd.Obj(reference))
}

// pd.Dic() queries dictionary data from a reference.
func (pd *PdfReaderT) Dic(reference []byte) map[string][]byte {
  return Dictionary(pd.Obj(reference))
}

// pd.Arr() queries array data from a reference.
func (pd *PdfReaderT) Arr(reference []byte) [][]byte {
  return Array(pd.Obj(reference))
}

// pd.ForcedArray() queries array data. If reference does not refer to an
// array, reference is taken as element of the returned array.
func (pd *PdfReaderT) ForcedArray(reference []byte) [][]byte {
  nr := pd.Obj(reference);
  if nr[0] != '[' {
    return [][]byte{reference}
  }
  return Array(nr);
}

// pd.Pages() returns an array with references to the pages of the PDF.
func (pd *PdfReaderT) Pages() [][]byte {
  if pd.pages != nil {
    return pd.pages
  }
  pages := pd.Dic(pd.Dic(pd.Trailer["/Root"])["/Pages"]);
  pd.pages = make([][]byte, pd.Num(pages["/Count"]));
  cp := 0;
  done := make(map[string]int);
  var q func(p [][]byte);
  q = func(p [][]byte) {
    for k := range p {
      if _, wrong := done[string(p[k])]; !wrong {
        done[string(p[k])] = 1;
        if kids, ok := pd.Dic(p[k])["/Kids"]; ok {
          q(pd.Arr(kids))
        } else {
          pd.pages[cp] = p[k];
          cp++;
        }
      } else {
        panic("Bad Page-Tree!")
      }
    }
  };
  q(pd.Arr(pages["/Kids"]));
  return pd.pages;
}

// pd.Attribute() tries to get an attribute definition from a page
// reference.  Note that the attribute definition is not resolved - so it's
// possible to get back a reference here.
func (pd *PdfReaderT) Attribute(a string, src []byte) []byte {
  d := pd.Dic(src);
  done := make(map[string]int);
  r, ok := d[a];
  for !ok {
    r, ok = d["/Parent"];
    if _, wrong := done[string(r)]; wrong || !ok {
      return _Bytes
    }
    done[string(r)] = 1;
    d = pd.Dic(r);
    r, ok = d[a];
  }
  return r;
}

// pd.Attribute() tries to get an attribute from a page reference.  The
// attribute will be resolved.
func (pd *PdfReaderT) Att(a string, src []byte) []byte {
  return pd.Obj(pd.Attribute(a, src))
}

// pd.Stream() returns contents of a stream.
func (pd *PdfReaderT) Stream(reference []byte) (map[string][]byte, []byte) {
  q, d := pd.Resolve(reference);
  dic := pd.Dic(d);
  pd.rdr.Seek(int64(q), 0);
  t, _ := simpleToken(pd.rdr);
  if string(t) != "stream" {
    return nil, []byte{}
  }
  skipLE(pd.rdr);
  data := make([]byte, pd.Num(dic["/Length"]));
  pd.rdr.Read(data);
  return dic, data;
}

// pd.DecodedStream() returns decoded contents of a stream.
func (pd *PdfReaderT) DecodedStream(reference []byte) (map[string][]byte, []byte) {
  dic, data := pd.Stream(reference);
  f, ok := dic["/Filter"];
  if ok {
    filter := pd.ForcedArray(f);
    for ff := range filter {
      switch string(filter[ff]) {
      case "/FlateDecode":
        inf, _ := zlib.NewInflater(bytes.NewBuffer(data));
        data, _ = io.ReadAll(inf);
        inf.Close();
      default:
        data = []byte{}
      }
    }
  }
  return dic, data;
}

// pd.PageFonts() returns references to the fonts defined for a page.
func (pd *PdfReaderT) PageFonts(page []byte) map[string][]byte {
  fonts, _ := pd.Dic(pd.Attribute("/Resources", page))["/Font"];
  if fonts == nil {
    return nil
  }
  return pd.Dic(fonts);
}

// Load() loads a PDF file of a given name.
func Load(fn string) *PdfReaderT {
  r := new(PdfReaderT);
  r.File = fn;
  dir, _ := os.Stat(fn);
  fil, _ := os.Open(fn, os.O_RDONLY, -1);
  r.rdr = fancy.SecReader(fil, int64(dir.Size));

  if r.Startxref = xrefStart(r.rdr); r.Startxref == -1 {
    return nil
  }
  if r.Xref = xrefRead(r.rdr, r.Startxref); r.Xref == nil {
    return nil
  }
  r.rdr.Seek(int64(xrefSkip(r.rdr, r.Startxref)), 0);
  s, _ := simpleToken(r.rdr);
  if string(s) != "trailer" {
    return nil
  }
  s, _ = simpleToken(r.rdr);
  if r.Trailer = Dictionary(s); r.Trailer == nil {
    return nil
  }
  r.rcache = make(map[string][]byte);
  r.rncache = make(map[string]int);
  return r;
}
