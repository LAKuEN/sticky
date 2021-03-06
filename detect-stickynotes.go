package stickynote

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sort"

	"gocv.io/x/gocv"
)

// StickyNote は付箋の検出位置を描画した画像と
// 切り抜いた画像の配列を内包する構造体です。
type StickyNote struct {
	DrawedImg   gocv.Mat
	CroppedImgs []gocv.Mat
}

// CutNDraw は画像内から付箋を検出し、付箋の位置を描画した画像と
// 付箋を切り抜いた画像を内包したStickyNote構造体を返します。
func CutNDraw(img gocv.Mat) (StickyNote, error) {
	// 前処理
	bgrChannels := gocv.Split(img)
	colorspaceChanged := img.Clone()
	gocv.CvtColor(colorspaceChanged, &colorspaceChanged, gocv.ColorBGRToYUV)
	yuvChannels := gocv.Split(colorspaceChanged)

	var preprocessedImgs []gocv.Mat
	for _, c := range []gocv.Mat{bgrChannels[1], yuvChannels[1], yuvChannels[2]} {
		// TODO 色差信号はヒストグラムが偏っているのでコントラストが低い
		// ヒストグラム平坦化を掛けてやれば陰影差が強調されそう
		preprocessedImgs = append(preprocessedImgs, preprocessingImg(c))
	}

	// 検出
	contours := [][]image.Point{}
	for _, img := range preprocessedImgs {
		cnts := gocv.FindContours(img, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		contours = append(contours, cnts...)
	}

	// 閾値処理
	var choosed [][]image.Point
	minSideLength := img.Size()[0]
	if minSideLength > img.Size()[1] {
		minSideLength = img.Size()[1]
	}
	// FIXME 下限サイズの指定をユーザーが行えるようにした方がよいかもしれない
	//       その場合、curlでの操作では厳しいので、WebページをIFとして作らないとまずい
	minSizeRatio := .1
	minSideLength = int(float64(minSideLength) * minSizeRatio)
	// FIXME アスペクト比で切ると過剰に除外されてしまうケースがあるのでとりあえず無効化
	//       いくつかのパターンを持たせて、それに合致するアスペクト比かを検査するように直す(そのうち)
	// aspectRatioThresh := 1.1 // 基底のアスペクト比からどの程度外れたものまで取るか
	for _, contour := range contours {
		minY, maxY, minX, maxX := extractMinMaxCoordinates(img, contour)
		lengthX := (maxX - minX)
		lengthY := (maxY - minY)
		if !isEnoughSizeRect(lengthX, lengthY, minSideLength, img) {
			continue
		}
		// if aspectRatioThresh < calcAspectRatio(lengthX, lengthY) {
		// 	continue
		// }
		// 4点の座標を左上から時計回りに格納
		c := []image.Point{{minX, minY}, {maxX, minY},
			{maxX, maxY}, {minX, maxY}}
		choosed = append(choosed, c)
	}
	choosed = combineContours(choosed)
	if len(choosed) < 1 {
		return StickyNote{DrawedImg: gocv.NewMat(), CroppedImgs: []gocv.Mat{}}, errors.New("Cannot detect any stickies")
	}

	// 画像の作成
	// * 矩形を描画した画像の作成
	drawed := img.Clone()
	gocv.DrawContours(&drawed, choosed, -1, color.RGBA{255, 0, 0, 255}, 3)
	// * 矩形範囲を切り抜いた画像の作成
	var imgs []gocv.Mat
	for _, c := range choosed {
		imgs = append(imgs,
			img.Region(image.Rectangle{c[0], c[2]}))
	}

	return StickyNote{DrawedImg: drawed, CroppedImgs: imgs}, nil
}

// calcAspectRatio はアスペクト比(短辺に対する長辺の長さの比)を計算します。
func calcAspectRatio(lengthX int, lengthY int) float64 {
	var longerSide, shorterSide float64
	if lengthY > lengthX {
		longerSide = float64(lengthY)
		shorterSide = float64(lengthX)
	} else {
		longerSide = float64(lengthX)
		shorterSide = float64(lengthY)
	}

	return (longerSide / shorterSide) // 短辺に対する長辺の長さの比
}

// extractMinMaxCoordinates はcontour内の最大・最小のx, y座標を返却します。
func extractMinMaxCoordinates(img gocv.Mat, contour []image.Point) (int, int, int, int) {
	minY := img.Size()[0]
	minX := img.Size()[1]
	maxY := 0
	maxX := 0
	for _, point := range contour {
		if point.X < minX {
			minX = point.X
		}
		if point.Y < minY {
			minY = point.Y
		}
		if point.X > maxX {
			maxX = point.X
		}
		if point.Y > maxY {
			maxY = point.Y
		}
	}

	return minY, maxY, minX, maxX
}

// generateIncContrastMat はgocv.LUTでコントラストを上げるgocv.Matを返却します。
func generateIncContrastMat() gocv.Mat {
	var incContrastBytes []byte
	lowerIntensity := 49.0
	upperIntensity := 205.0
	incVal := 255.0 / (upperIntensity - lowerIntensity)
	for i := 0; i < 256; i++ {
		if float64(i) < lowerIntensity {
			incContrastBytes = append(incContrastBytes, byte(0.0))
			continue
		}
		if float64(i) > upperIntensity {
			incContrastBytes = append(incContrastBytes, byte(255.0))
			continue
		}
		incContrastBytes = append(incContrastBytes, byte((float64(i)-49.0)*incVal))
	}
	incContrastMat, _ := gocv.NewMatFromBytes(1, 256, gocv.MatTypeCV8U, incContrastBytes)

	return incContrastMat
}

// combineContours は包含関係にある領域を統合します。
func combineContours(contours [][]image.Point) [][]image.Point {
	var dstContours [][]image.Point
	var processedIndices []int
	coef := 0.2 // おおよそ重なっているときにも統合したい場合、0以上の値を設定
	for mIdx, mContour := range contours {
		if contains(processedIndices, mIdx) {
			continue
		}
		processedIndices = append(processedIndices, mIdx)
		// 少なくとも2つ以上の座標が取得できていない場合は除外
		if !isEnoughPointsInContour(mContour) {
			continue
		}
		dstContour := mContour
		dstContourMin := dstContour[0]
		dstContourMax := dstContour[2]
		for tIdx, tContour := range contours {
			if !isEnoughPointsInContour(tContour) {
				processedIndices = append(processedIndices, tIdx)
				continue
			}
			tContourMin := tContour[0]
			tContourMax := tContour[2]

			// 包含関係にあるものを統合
			// おおよそ重なっているものを統合する為に係数coefを掛けている
			dstMinX := int(float64(dstContourMin.X) * (1 - coef))
			dstMinY := int(float64(dstContourMin.Y) * (1 - coef))
			dstMaxX := int(float64(dstContourMax.X) * (1 + coef))
			dstMaxY := int(float64(dstContourMax.Y) * (1 + coef))
			if (dstMinX <= tContourMin.X) && (dstMinY <= tContourMin.Y) &&
				(dstMaxX >= tContourMax.X) && (dstMaxY >= tContourMax.Y) {
				// tContourがdstContourに包含される: tContourを処理済みに
				processedIndices = append(processedIndices, tIdx)
			} else if (dstMinX > tContourMin.X) && (dstMinY > tContourMin.Y) &&
				(dstMaxX < tContourMax.X) && (dstMaxY < tContourMax.Y) {
				// dstContourがtContourに包含される: tContourをdstContourに代入
				dstContour = tContour
				dstContourMin = tContour[0]
				dstContourMax = tContour[2]
				processedIndices = append(processedIndices, tIdx)
			} else {
				// 包含関係にない
				continue
			}
		}
		dstContours = append(dstContours, dstContour)
	}

	return dstContours
}

// contains は配列内に該当するint要素が含まれているかを検査します。
func contains(s []int, e int) bool {
	for _, v := range s {
		if e == v {
			return true
		}
	}
	return false
}

// getMidValue はint配列から中央値を取得します。
func getMidValue(values []int) int {
	midIdx := int(math.Ceil(float64(len(values)) / 2))
	sort.SliceStable(values, func(i, j int) bool {
		return values[i] > values[j]
	})

	return values[midIdx]
}

// isEnoughPointsInContour は配列に2つ以上のimage.Pointが含まれるか判定します。
func isEnoughPointsInContour(contour []image.Point) bool {
	if len(contour) < 2 {
		return false
	}
	return true
}

// isEnoughSizeRect はx, yの辺の長さが指定サイズ以上、画像サイズ以下か検査します。
func isEnoughSizeRect(lengthX, lengthY, minSideLength int, img gocv.Mat) bool {
	if lengthX <= minSideLength || lengthX >= img.Size()[0]-2 ||
		lengthY <= minSideLength || lengthY >= img.Size()[1]-2 {
		return false
	}
	return true
}

// preprocessingImg は画像に規定の前処理を施して返却します。
func preprocessingImg(grayImg gocv.Mat) gocv.Mat {
	preprocessed := grayImg.Clone()
	gocv.AdaptiveThreshold(preprocessed, &preprocessed, 255,
		gocv.AdaptiveThresholdGaussian,
		gocv.ThresholdBinaryInv, 51, 1)

	return preprocessed
}

// func getLUT() gocv.Mat {
// 	l := gocv.NewMatWithSize(1, 256, gocv.MatTypeCV8U)
// 	thresh := 50
// 	steps := 255 - thresh
// 	inc := 255.0 / float32(steps)
// 	for i := 0; i < 256; i++ {
// 		if i < thresh {
// 			continue
// 		}
// 		v := uint8(inc * (float32(i) - float32(thresh)))
// 		l.SetUCharAt(0, i, v) // 1段階ごとの増分*指定範囲を0~255と見做したときのindex
// 	}
//
// 	return l
// }
