// bg/modality_challenge.go — 多模态「语义级」判别挑战的生成与判读
// （迁移 825 的证据来源）。
//
// # 为什么需要一个挑战，而不是把 HTTP 200 当证据
//
// bg/probe_modality.go 的 ProbeModality 在 2xx 时无条件 Supported=true。
// 但纯文本模型经中转接入时经常**收下** image_url 内容块、返回 200、
// 然后完全无视那张图并自由发挥。所以：
//
//	「接口收下了这个模态」≠「模型看得见」
//
// 前者是结构级（可承载，carry_level），后者是语义级（真能读，
// read_level）。本文件只负责产生后者的证据。
//
// # 挑战怎么设计才不会自欺
//
// 判据必须是「模型答对了，而一个看不见图的模型几乎不可能答对」。
// 于是：
//
//   - 色板只有 8 个高区分度的颜色，且**刻意不含** navy/teal/grey/
//     cyan/pink 这类易混色。宁可让色板窄，也不要假阴性：假阴性会把
//     一个能用的 vision 模型降级成 text，而 text 会被候选过滤排除
//     ⇒ 图片请求 503 no_candidate。这是比误标更贵的失效。
//   - 一次挑战 = 4 个互不相同的色块，要求**按从左到右的顺序**全部
//     说对。盲猜概率 = 1/(8·7·6·5) = 1/1680。
//   - 判定要求连续两次不同挑战都答对（两胜定「真能读」，见
//     modality_verification.go 的 attempt 口径）⇒ 盲模型两次蒙对的
//     概率 ≈ 3.5e-7。
//   - 分隔带用灰色（不在色板内，模型即使提到也会被判读器忽略），
//     避免相邻色块边界含糊。
//
// # 判读的保守性
//
// 回答里颜色词不足 4 个时**不下结论**（inconclusive），因为那既可能是
// 「看不见所以拒答」，也可能是「推理模型把 token 花光了、正文被截断」。
// 把 inconclusive 记成 negative 就等于用默认值关掉一个可能完全正常的
// 绑定——这是 bg/capability_backfill.go 第 2 条不可让步约束的同一条
// 纪律。
//
// # 音频为什么没有语义挑战
//
// 语义级判据需要一个**可程序化生成的正确答案**。图像可以用随机色块
// 造，语音不行：仓库里没有 TTS 依赖，也不该为了探针引入一个。
// 「发一段静音看它返回什么」测的是接口形状不是听觉能力；「发 N 声
// 哔哔数个数」会被 ASR 家族稳定判负（它们做的是转写不是计数），
// 那是假阴性而不是准确。
//
// 所以音频只落 carry_level，read_level 恒为 unknown，直到有一份
// 带标注的语音语料（见 audioSemanticChallengeRequired 处的说明）。
// 这是一个已知的诚实边界，不是遗漏。
package bg

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	mathrand "math/rand/v2"
	"sort"
	"strings"
	"time"
	"unicode"
)

// visionChallengeBlocks 是每次挑战的色块数。要求「全部按顺序答对」，
// 所以块数直接决定盲猜概率：4 块 / 8 色 ⇒ 1/1680。
const visionChallengeBlocks = 4

// challengeBlockPx 是单个色块的边长。32px 足够模型看清纯色块，
// 又让整图（4×32 + 3×4 分隔 = 140×32）压到几百字节。
const challengeBlockPx = 32

// challengeSeparatorPx 是色块之间的灰色分隔带宽度。灰色不在色板内，
// 判读器不认它，所以模型顺带提到「灰色」不会影响判分。
const challengeSeparatorPx = 4

// VisionChallenge 是一次语义判别挑战。Colors 是这张图里从左到右的
// 正确答案，DataURL 是要发给上游的 data:image/png URL。
type VisionChallenge struct {
	Colors  []string
	DataURL string
}

// challengePalette 是 8 个高区分度颜色。刻意窄：见文件头「宁可让色板
// 窄，也不要假阴性」。
var challengePalette = []struct {
	name string
	rgb  color.RGBA
}{
	{"red", color.RGBA{0xFF, 0x00, 0x00, 0xFF}},
	{"green", color.RGBA{0x00, 0xC0, 0x00, 0xFF}},
	{"blue", color.RGBA{0x00, 0x00, 0xFF, 0xFF}},
	{"yellow", color.RGBA{0xFF, 0xFF, 0x00, 0xFF}},
	{"black", color.RGBA{0x00, 0x00, 0x00, 0xFF}},
	{"white", color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}},
	{"orange", color.RGBA{0xFF, 0xA5, 0x00, 0xFF}},
	{"purple", color.RGBA{0x80, 0x00, 0x80, 0xFF}},
}

// challengeGray 是分隔带颜色。**不在色板内**。
var challengeGray = color.RGBA{0x80, 0x80, 0x80, 0xFF}

// visionChallengePrompt 是随挑战发出的提问。明确要求英文色名并按顺序
// 回答，同时接受中文回答（判读器两种都认，见 normalizeColorWord）。
const visionChallengePrompt = "This image is a single row of 4 solid color blocks, separated by thin gray bars. " +
	"Answer with ONLY the 4 color names separated by commas, in left-to-right order. " +
	"No other words."

// NewVisionChallenge 生成一次挑战。rng 可注入，便于测试复现；
// 生产用 newVisionChallengeRNG()。
func NewVisionChallenge(rng *mathrand.Rand) (VisionChallenge, error) {
	picked, err := pickDistinctColors(rng, visionChallengeBlocks)
	if err != nil {
		return VisionChallenge{}, err
	}
	img := renderChallengeImage(picked)
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		return VisionChallenge{}, fmt.Errorf("encode challenge png: %w", err)
	}
	return VisionChallenge{
		Colors:  picked,
		DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// newVisionChallengeRNG 用 crypto/rand 播种 math/rand。挑战每次都不同，
// 否则同一个模型只需要记住上一次那张图。
//
// 挑战可预测不构成安全边界——最坏情况是盲模型撞对上一次那张图，
// 概率仍是 1/1680。所以 crypto/rand 失败时退化到时间派生种子是可接受
// 的，不需要把探测路径变成一个会因熵源故障而起不来的任务。
func newVisionChallengeRNG() *mathrand.Rand {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		n := uint64(time.Now().UnixNano())
		return mathrand.New(mathrand.NewPCG(n, n^0x9e3779b97f4a7c15))
	}
	return mathrand.New(mathrand.NewPCG(
		binary.LittleEndian.Uint64(seed[0:8]),
		binary.LittleEndian.Uint64(seed[8:16]),
	))
}

// pickDistinctColors 从色板里不重复地取 n 个颜色。
func pickDistinctColors(rng *mathrand.Rand, n int) ([]string, error) {
	if n > len(challengePalette) {
		return nil, fmt.Errorf("challenge needs %d colors, palette has %d", n, len(challengePalette))
	}
	idx := rng.Perm(len(challengePalette))[:n]
	out := make([]string, n)
	for i, j := range idx {
		out[i] = challengePalette[j].name
	}
	return out, nil
}

// renderChallengeImage 画出「色块 + 灰色分隔带」的单行图。
func renderChallengeImage(order []string) image.Image {
	width := len(order)*challengeBlockPx + (len(order)-1)*challengeSeparatorPx
	img := image.NewRGBA(image.Rect(0, 0, width, challengeBlockPx))

	// 先铺灰底，未被色块覆盖处就是分隔带。
	for x := 0; x < width; x++ {
		for y := 0; y < challengeBlockPx; y++ {
			img.Set(x, y, challengeGray)
		}
	}
	byName := make(map[string]color.RGBA, len(challengePalette))
	for _, c := range challengePalette {
		byName[c.name] = c.rgb
	}
	for i, name := range order {
		rgb, ok := byName[name]
		if !ok {
			// 不可能发生：order 来自 pickDistinctColors。
			continue
		}
		x0 := i * (challengeBlockPx + challengeSeparatorPx)
		for x := x0; x < x0+challengeBlockPx; x++ {
			for y := 0; y < challengeBlockPx; y++ {
				img.Set(x, y, rgb)
			}
		}
	}
	return img
}

// GradeVisionAnswer 判读模型对一次挑战的回答。
//
// 返回值（matched 与 score 是两个不同的数，别混用）：
//
//   - matched：正确答案的 4 个颜色是否**按顺序**出现在模型提到的颜色
//     序列里。这是唯一的定论依据。宽容到允许中间夹话（"第一个是红色
//     的，第二个是绿色的…"），因为信息本身就在回答里；但顺序错就是错。
//   - score：**按位置**命中的个数（0..4），仅用于证据留痕。一个
//     顺序完全错但颜色全对的回答，score 会是 0——这正是判读失败时
//     最需要一眼看出的形态。
//   - mentioned：回答里识别到的颜色词序列（已归一化），便于人工复核
//     「它到底说了什么」。
//
// 颜色词不足 4 个时 matched=false 且 mentioned 少于 4 个，调用方必须把
// 这当作 inconclusive（unknown），**不能**当作 negative。见文件头。
func GradeVisionAnswer(answer string, challenge VisionChallenge) (matched bool, score int, mentioned []string) {
	mentioned = mentionedColors(answer)
	if len(challenge.Colors) == 0 {
		return false, 0, mentioned
	}

	// 位置分：只用于留痕。
	for i := 0; i < len(challenge.Colors) && i < len(mentioned); i++ {
		if mentioned[i] == challenge.Colors[i] {
			score++
		}
	}

	// 定论：正确答案作为 mentioned 的**顺序子序列**出现。
	pos := 0
	for _, want := range challenge.Colors {
		found := false
		for pos < len(mentioned) {
			if mentioned[pos] == want {
				pos++
				found = true
				break
			}
			pos++
		}
		if !found {
			return false, score, mentioned
		}
	}
	return true, score, mentioned
}

// colorSynonyms 把可能出现的各种写法归一到 8 个色板名。中文一并认，
// 因为要求「答英文」是提示词层面的软约束，模型用中文回答很常见；
// 只认英文会把正常的 vision 模型判成看不见。
//
// **刻意不收 gold / 银 这类跨色板的歧义词**：#FFFF00（黄）与
// #FFA500（橙）都被叫过 gold，收到 yellow 或 orange 任一边都会把
// 一次正确回答变成假阴性。歧义写法宁可让判读落进 inconclusive
// （unknown，不升级也不降级），也不要猜。
var colorSynonyms = map[string]string{
	"red": "red", "crimson": "red", "scarlet": "red", "红": "red", "红色": "red",
	"green": "green", "lime": "green", "emerald": "green", "绿": "green", "绿色": "green",
	"blue": "blue", "蓝": "blue", "蓝色": "blue",
	"yellow": "yellow", "黄": "yellow", "黄色": "yellow",
	"black": "black", "黑": "black", "黑色": "black",
	"white": "white", "白": "white", "白色": "white",
	"orange": "orange", "amber": "orange", "橙": "orange", "橙色": "orange",
	"purple": "purple", "violet": "purple", "紫": "purple", "紫色": "purple",
}

// colorKeysByLenDesc 是 colorSynonyms 的键按「字符数降序」排好的缓存。
//
// 最长优先是必须的：色板里同时有 "红" 和 "红色"、"orange" 和
// "orange-ish"。短优先会把 "红色" 切成 "红" + 一个匹配不上的 "色"，
// 丢掉后面真正的答案。
var colorKeysByLenDesc = func() []string {
	keys := make([]string, 0, len(colorSynonyms))
	for k := range colorSynonyms {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return len([]rune(keys[i])) > len([]rune(keys[j]))
	})
	return keys
}()

// mentionedColors 从自由文本里按出现顺序抽出归一化后的颜色词。
//
// 中文没有词边界，所以不能按空格切词（"红、绿、蓝" 里逗号能切，
// "红色的方块" 的"的"切不掉）。做法：把文本扫成两类极大连续段——
// 拉丁字母段与汉字段——再逐段向前做最长已知词匹配。
//
// 重复提及**不去重**：模型先说错再改口时顺序里会出现错色，那是判读
// 失败时唯一能看出「它到底说了什么」的信息，抹掉就查不了了。
func mentionedColors(answer string) []string {
	lower := strings.ToLower(answer)
	var out []string
	runes := []rune(lower)

	i := 0
	for i < len(runes) {
		switch {
		case isLatinLetter(runes[i]):
			j := i
			for j < len(runes) && isLatinLetter(runes[j]) {
				j++
			}
			out = append(out, matchColorRun(runes[i:j])...)
			i = j
		case unicode.Is(unicode.Han, runes[i]):
			j := i
			for j < len(runes) && unicode.Is(unicode.Han, runes[j]) {
				j++
			}
			out = append(out, matchColorRun(runes[i:j])...)
			i = j
		default:
			i++
		}
	}
	return out
}

func isLatinLetter(r rune) bool {
	return r >= 'a' && r <= 'z'
}

// matchColorRun 在一段连续同种文字（拉丁或汉字）里向前做最长已知颜色词
// 匹配，命中即前进，未命中前进一个字符。
//
// 拉丁段也走最长前缀，是为了容忍 "redgreenblueyellow" 这种极简回答。
func matchColorRun(run []rune) []string {
	var out []string
	for i := 0; i < len(run); {
		matched := false
		for _, key := range colorKeysByLenDesc {
			kr := []rune(key)
			if i+len(kr) > len(run) {
				continue
			}
			if string(run[i:i+len(kr)]) != key {
				continue
			}
			out = append(out, colorSynonyms[key])
			i += len(kr)
			matched = true
			break
		}
		if !matched {
			i++
		}
	}
	return out
}
