package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func resizeImage(body []byte, upstreamType, mode string) (PreparedImage, error) {
	typeName := imageType(body, upstreamType)
	if typeName == "" {
		return PreparedImage{}, errors.New("upstream is not an image")
	}
	if typeName == "image/gif" {
		width, height, frames := gifSizeAndFrames(body)
		if width <= 0 || height <= 0 || frames <= 0 {
			return PreparedImage{}, errors.New("unreadable GIF image")
		}
		pending := int64(width)*int64(height)*int64(frames) > 0 && int64(width)*int64(height)*int64(frames) <= maxAnimationPixels
		return PreparedImage{Body: body, Type: typeName, PendingAnimation: pending}, nil
	}
	if typeName == "image/webp" && webpAnimated(body) || typeName == "image/avif" && avifAnimated(body) {
		return PreparedImage{Body: body, Type: typeName}, nil
	}
	avatar := mode == "avatar" || strings.HasPrefix(mode, "avatar-")
	width := renditionWidth(mode)
	if typeName == "image/svg+xml" && !avatar && len(body) <= 128*1024 {
		return PreparedImage{Body: body, Type: typeName}, nil
	}
	var decoded image.Image
	var err error
	switch typeName {
	case "image/svg+xml":
		decoded, err = rasterizeSVG(body, width)
	case "image/x-icon":
		decoded, err = decodeICO(body)
	case "image/webp":
		var cfg image.Config
		cfg, err = webp.DecodeConfig(bytes.NewReader(body))
		if err == nil && validImageSize(cfg.Width, cfg.Height) {
			decoded, err = webp.Decode(bytes.NewReader(body), webp.Options{AutoRotate: true})
		} else if err == nil {
			err = errors.New("image dimensions exceed limit")
		}
	case "image/avif":
		var cfg image.Config
		cfg, err = avif.DecodeConfig(bytes.NewReader(body))
		if err == nil && validImageSize(cfg.Width, cfg.Height) {
			decoded, err = avif.Decode(bytes.NewReader(body), avif.Options{AutoRotate: true})
		} else if err == nil {
			err = errors.New("image dimensions exceed limit")
		}
	default:
		var cfg image.Config
		cfg, _, err = image.DecodeConfig(bytes.NewReader(body))
		if err == nil && validImageSize(cfg.Width, cfg.Height) {
			if typeName == "image/jpeg" {
				decoded, err = imaging.Decode(bytes.NewReader(body), imaging.AutoOrientation(true))
			} else {
				decoded, _, err = image.Decode(bytes.NewReader(body))
			}
		} else if err == nil {
			err = errors.New("image dimensions exceed limit")
		}
	}
	if err != nil {
		return PreparedImage{}, err
	}
	if decoded == nil {
		return PreparedImage{}, errors.New("image decoder returned no pixels")
	}
	resized := resizePixels(decoded, width, avatar)
	switch typeName {
	case "image/png", "image/x-icon", "image/svg+xml":
		encoded, err := encodeWebP(resized, 88)
		return PreparedImage{Body: encoded, Type: "image/webp"}, err
	case "image/webp":
		encoded, err := encodeWebP(resized, 82)
		return PreparedImage{Body: encoded, Type: "image/webp"}, err
	default:
		var out bytes.Buffer
		err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: 82})
		return PreparedImage{Body: out.Bytes(), Type: "image/jpeg"}, err
	}
}

func imageType(body []byte, upstreamType string) string {
	supplied := strings.ToLower(strings.TrimSpace(strings.SplitN(upstreamType, ";", 2)[0]))
	switch {
	case bytes.HasPrefix(body, pngSignature):
		return "image/png"
	case len(body) > 3 && body[0] == 0xff && body[1] == 0xd8 && body[2] == 0xff:
		return "image/jpeg"
	case len(body) >= 6 && (string(body[:6]) == "GIF87a" || string(body[:6]) == "GIF89a"):
		return "image/gif"
	case len(body) >= 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "WEBP":
		return "image/webp"
	case len(body) >= 12 && string(body[4:8]) == "ftyp" && (bytes.Contains(body[8:min(len(body), 64)], []byte("avif")) || bytes.Contains(body[8:min(len(body), 64)], []byte("avis"))):
		return "image/avif"
	case len(body) >= 2 && string(body[:2]) == "BM":
		return "image/bmp"
	case len(body) >= 4 && (bytes.Equal(body[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(body[:4], []byte{'M', 'M', 0, 42})):
		return "image/tiff"
	case len(body) >= 4 && body[0] == 0 && body[1] == 0 && body[2] == 1 && body[3] == 0:
		return "image/x-icon"
	case looksLikeSVG(body):
		return "image/svg+xml"
	case strings.HasPrefix(supplied, "image/"):
		return supplied
	default:
		return ""
	}
}

func looksLikeSVG(body []byte) bool {
	text := strings.TrimSpace(strings.TrimPrefix(string(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})), "<?xml"))
	if index := strings.Index(text, "<svg"); index >= 0 && index < 4096 {
		return true
	}
	return false
}

func validImageSize(width, height int) bool {
	return width > 0 && height > 0 && int64(width)*int64(height) <= maxAnimationPixels
}

func renditionWidth(mode string) int {
	widths := map[string]int{"avatar": 96, "card": 336, "thumb": 720, "full": 1600, "og": 1200, "avatar-48": 48, "avatar-96": 96, "image-336": 336, "image-720": 720, "image-1200": 1200, "image-1600": 1600}
	if width := widths[mode]; width > 0 {
		return width
	}
	return 1600
}

func resizePixels(source image.Image, width int, avatar bool) image.Image {
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if avatar {
		return imaging.Fill(source, width, width, imaging.Center, imaging.Lanczos)
	}
	if w <= width {
		return source
	}
	height := int(math.Round(float64(h) * float64(width) / float64(w)))
	if height < 1 {
		height = 1
	}
	return imaging.Resize(source, width, height, imaging.Lanczos)
}

func rasterizeSVG(body []byte, width int) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(body), oksvg.IgnoreErrorMode)
	if err != nil || icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil, errors.New("unreadable SVG image")
	}
	targetW, targetH := width, int(math.Round(float64(width)*icon.ViewBox.H/icon.ViewBox.W))
	if targetH < 1 {
		targetH = 1
	}
	if targetW > 4096 || targetH > 4096 || int64(targetW)*int64(targetH) > maxAnimationPixels {
		return nil, errors.New("SVG dimensions exceed limit")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	icon.SetTarget(0, 0, float64(targetW), float64(targetH))
	scanner := rasterx.NewScannerGV(targetW, targetH, canvas, canvas.Bounds())
	icon.Draw(rasterx.NewDasher(targetW, targetH, scanner), 1)
	return canvas, nil
}

func encodeWebP(source image.Image, quality int) ([]byte, error) {
	var out bytes.Buffer
	if err := webp.Encode(&out, source, webp.Options{Quality: quality, Method: 4}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodeICO(body []byte) (image.Image, error) {
	if len(body) < 6 || body[0] != 0 || body[1] != 0 || body[2] != 1 || body[3] != 0 {
		return nil, errors.New("unreadable icon")
	}
	entries := int(body[4]) | int(body[5])<<8
	type entry struct{ size, bytes, offset int }
	list := make([]entry, 0, entries)
	for i := 0; i < entries; i++ {
		at := 6 + i*16
		if at+16 > len(body) {
			break
		}
		size := int(body[at])
		if size == 0 {
			size = 256
		}
		byteCount := int(uint32(body[at+8]) | uint32(body[at+9])<<8 | uint32(body[at+10])<<16 | uint32(body[at+11])<<24)
		offset := int(uint32(body[at+12]) | uint32(body[at+13])<<8 | uint32(body[at+14])<<16 | uint32(body[at+15])<<24)
		if byteCount > 0 && offset >= 0 && offset+byteCount <= len(body) {
			list = append(list, entry{size, byteCount, offset})
		}
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].size > list[i].size {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	for _, item := range list {
		data := body[item.offset : item.offset+item.bytes]
		if bytes.HasPrefix(data, pngSignature) {
			img, _, err := image.Decode(bytes.NewReader(data))
			if err == nil {
				return img, nil
			}
			continue
		}
		if len(data) < 40 {
			continue
		}
		width := int(int32(uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16 | uint32(data[7])<<24))
		rawHeight := int(int32(uint32(data[8]) | uint32(data[9])<<8 | uint32(data[10])<<16 | uint32(data[11])<<24))
		if rawHeight < 0 {
			rawHeight = -rawHeight
		}
		height, bits := rawHeight/2, int(data[14])|int(data[15])<<8
		if width <= 0 || height <= 0 || (bits != 24 && bits != 32) || !validImageSize(width, height) {
			continue
		}
		pixelOffset := int(uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24)
		stride := ((width*bits + 31) / 32) * 4
		maskStride := ((width + 31) / 32) * 4
		if pixelOffset < 40 || pixelOffset+stride*height > len(data) {
			continue
		}
		result := image.NewNRGBA(image.Rect(0, 0, width, height))
		allAlphaZero := bits == 32
		for y := 0; y < height; y++ {
			row := data[pixelOffset+(height-1-y)*stride:]
			for x := 0; x < width; x++ {
				p, out := x*(bits/8), result.PixOffset(x, y)
				result.Pix[out], result.Pix[out+1], result.Pix[out+2] = row[p+2], row[p+1], row[p]
				alpha := byte(255)
				if bits == 32 {
					alpha = row[p+3]
					allAlphaZero = allAlphaZero && alpha == 0
				}
				result.Pix[out+3] = alpha
			}
		}
		if allAlphaZero {
			maskAt := pixelOffset + stride*height
			if maskAt+maskStride*height > len(data) {
				for i := 3; i < len(result.Pix); i += 4 {
					result.Pix[i] = 255
				}
				return result, nil
			}
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					mask := data[maskAt+(height-1-y)*maskStride+x/8]&(1<<uint(7-x%8)) != 0
					if mask {
						result.Pix[result.PixOffset(x, y)+3] = 0
					} else {
						result.Pix[result.PixOffset(x, y)+3] = 255
					}
				}
			}
		}
		return result, nil
	}
	return nil, errors.New("unreadable icon")
}

func gifSizeAndFrames(body []byte) (int, int, int) {
	if len(body) < 13 || (string(body[:6]) != "GIF87a" && string(body[:6]) != "GIF89a") {
		return 0, 0, 0
	}
	w, h := int(body[6])|int(body[7])<<8, int(body[8])|int(body[9])<<8
	if !validImageSize(w, h) {
		return w, h, 0
	}
	packed, at, frames := body[10], 13, 0
	if packed&0x80 != 0 {
		at += 3 * (1 << ((packed & 0x07) + 1))
	}
	for at < len(body) {
		switch body[at] {
		case 0x3b:
			return w, h, frames
		case 0x21:
			at++
			if at >= len(body) {
				return w, h, 0
			}
			at++
			var ok bool
			at, ok = skipGIFBlocks(body, at)
			if !ok {
				return w, h, 0
			}
		case 0x2c:
			if at+10 > len(body) {
				return w, h, 0
			}
			packed := body[at+9]
			at += 10
			if packed&0x80 != 0 {
				at += 3 * (1 << ((packed & 0x07) + 1))
			}
			if at >= len(body) {
				return w, h, 0
			}
			at++
			var ok bool
			at, ok = skipGIFBlocks(body, at)
			if !ok {
				return w, h, 0
			}
			frames++
		default:
			return w, h, 0
		}
	}
	return w, h, 0
}

func skipGIFBlocks(body []byte, at int) (int, bool) {
	for at < len(body) {
		size := int(body[at])
		at++
		if size == 0 {
			return at, true
		}
		if at+size > len(body) {
			return at, false
		}
		at += size
	}
	return at, false
}

func webpAnimated(body []byte) bool {
	for at := 12; at+8 <= len(body); {
		kind, size := string(body[at:at+4]), int(uint32(body[at+4])|uint32(body[at+5])<<8|uint32(body[at+6])<<16|uint32(body[at+7])<<24)
		if kind == "ANIM" || kind == "ANMF" {
			return true
		}
		at += 8 + size + size%2
	}
	return false
}

func avifAnimated(body []byte) bool {
	return len(body) >= 12 && string(body[4:8]) == "ftyp" && string(body[8:12]) == "avis"
}

func ConvertAnimated(dataDir, rawURL, mode string) (int64, error) {
	return ConvertAnimatedContext(context.Background(), dataDir, rawURL, mode)
}

func ConvertAnimatedContext(ctx context.Context, dataDir, rawURL, mode string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	key := cacheKey(rawURL, mode)
	file := filepath.Join(dataDir, "imgcache", key[:2], key)
	body, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	typeBytes, err := os.ReadFile(file + ".type")
	if err != nil || strings.TrimSpace(string(typeBytes)) != "image/gif" {
		return 0, err
	}
	if _, err := os.Stat(file + ".prepared"); err == nil {
		return 0, nil
	}
	w, h, frames := gifSizeAndFrames(body)
	if int64(w)*int64(h)*int64(frames) == 0 || int64(w)*int64(h)*int64(frames) > maxAnimationPixels {
		return 0, nil
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(body))
	if err != nil || len(decoded.Image) != frames {
		if err == nil {
			err = errors.New("animated GIF frame count changed")
		}
		return 0, err
	}
	width := renditionWidth(mode)
	canvasRect := image.Rect(0, 0, decoded.Config.Width, decoded.Config.Height)
	canvas := image.NewNRGBA(canvasRect)
	previousRect := image.Rectangle{}
	previousDisposal := byte(0)
	var previousBefore *image.NRGBA
	loopCount := decoded.LoopCount
	if loopCount < 0 {
		loopCount = 1
	}
	webpImage := &webp.WEBP{LoopCount: loopCount, Image: make([]image.Image, 0, frames), Delay: make([]int, 0, frames)}
	for i, frame := range decoded.Image {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if i > 0 {
			switch previousDisposal {
			case gif.DisposalBackground:
				draw.Draw(canvas, previousRect, image.NewUniform(color.Transparent), image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				if previousBefore != nil {
					canvas = imaging.Clone(previousBefore)
				}
			}
		}
		before := imaging.Clone(canvas)
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		webpImage.Image = append(webpImage.Image, imaging.Clone(resizePixels(canvas, width, false)))
		delay := 100
		if i < len(decoded.Delay) && decoded.Delay[i] > 0 {
			delay = decoded.Delay[i] * 10
		}
		webpImage.Delay = append(webpImage.Delay, delay)
		previousRect, previousBefore = frame.Bounds(), before
		if i < len(decoded.Disposal) {
			previousDisposal = decoded.Disposal[i]
		} else {
			previousDisposal = gif.DisposalNone
		}
	}
	var out bytes.Buffer
	if err := webp.EncodeAll(&out, webpImage, webp.Options{Quality: 80, Method: 4}); err != nil {
		return 0, err
	}
	if int64(out.Len()) > int64(float64(len(body))*0.85) {
		return 0, atomicWrite(file+".prepared", []byte("original"))
	}
	if err := atomicWrite(file, out.Bytes()); err != nil {
		return 0, err
	}
	if err := atomicWrite(file+".type", []byte("image/webp")); err != nil {
		return 0, err
	}
	return int64(len(body) - out.Len()), nil
}
