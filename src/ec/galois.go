package ec

// GF256 表示 GF(2^8) 有限域，基于不可约多项式 x^8 + x^4 + x^3 + x^2 + 1（0x11D）。
// 与 RAID-6 和 Cauchy Reed-Solomon 使用相同的多项式。
// 通过预计算指数表和对数表实现高效乘除法。
type GF256 struct {
	expTable [512]byte // 指数表（2x 循环，避免取模）
	logTable [256]byte // 对数表
}

// 不可约多项式 0x11D = x^8 + x^4 + x^3 + x^2 + 1。
const irrPoly = 0x11D

// NewGF256 创建 GF(2^8) 实例，预计算 exp/log 查找表。
func NewGF256() *GF256 {
	gf := &GF256{}
	x := byte(1)
	for i := 0; i < 255; i++ {
		gf.expTable[i] = x
		gf.logTable[x] = byte(i)
		x = gf.mul(x, 2)
	}
	// expTable 后半部分是前半部分的副本，避免 Mul 中的取模运算。
	for i := 255; i < 512; i++ {
		gf.expTable[i] = gf.expTable[i-255]
	}
	return gf
}

// Add 两个 GF(2^8) 元素的加法（即 XOR）。
func (gf *GF256) Add(a, b byte) byte {
	return a ^ b
}

// Sub 两个 GF(2^8) 元素的减法（与加法相同，即 XOR）。
func (gf *GF256) Sub(a, b byte) byte {
	return a ^ b
}

// Mul 两个 GF(2^8) 元素的乘法。
// 利用 exp/log 表：a*b = exp[log[a] + log[b]]。
func (gf *GF256) Mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gf.expTable[int(gf.logTable[a])+int(gf.logTable[b])]
}

// Div 两个 GF(2^8) 元素的除法。
// 利用 exp/log 表：a/b = exp[log[a] - log[b]]。
func (gf *GF256) Div(a, b byte) byte {
	if b == 0 {
		panic("galois: division by zero")
	}
	if a == 0 {
		return 0
	}
	return gf.expTable[int(gf.logTable[a])-int(gf.logTable[b])+255]
}

// Inv 计算 GF(2^8) 元素的乘法逆元。
// 利用 exp/log 表：a^(-1) = exp[255 - log[a]]。
func (gf *GF256) Inv(a byte) byte {
	if a == 0 {
		panic("galois: zero has no inverse")
	}
	return gf.expTable[255-int(gf.logTable[a])]
}

// mul 执行 GF(2^8) 多项式乘法（无表版本，仅用于初始化 exp 表）。
func (gf *GF256) mul(a, b byte) byte {
	var result byte
	for i := 0; i < 8; i++ {
		if (b & 1) != 0 {
			result ^= a
		}
		hi := a & 0x80
		a <<= 1
		if hi != 0 {
			a ^= byte(irrPoly & 0xFF) // XOR 低 8 位
		}
		b >>= 1
	}
	return result
}
