// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package number

import (
	"strconv"
	"unicode/utf8"

	"golang.org/x/text/language"
)

// TODO:
// - grouping of fractions
// - allow user-defined superscript notation (such as <sup>4</sup>)
// - same for non-breaking spaces, like &nbsp;

// Formatting proceeds along the following lines:
// 0) Compose rounding information from format and context.
// 1) Convert a number into a Decimal.
// 2) Sanitize Decimal by adding trailing zeros, removing leading digits, and
//    (non-increment) rounding. The Decimal that results from this is suitable
//    for determining the plural form.
// 3) Render the Decimal in the localized form.

// Formatter contains all the information needed to render a number.
type Formatter struct {
	Pattern
	Info
	RoundingContext
}

func (f *Formatter) init(t language.Tag, index []uint8) {
	f.Info = InfoFromTag(t)
	for ; ; t = t.Parent() {
		if ci, ok := language.CompactIndex(t); ok {
			f.Pattern = formats[index[ci]]
			break
		}
	}
}

// InitPattern initializes a Formatter for the given Pattern.
func (f *Formatter) InitPattern(t language.Tag, pat *Pattern) {
	f.Info = InfoFromTag(t)
	f.Pattern = *pat
}

// InitDecimal initializes a Formatter using the default Pattern for the given
// language.
func (f *Formatter) InitDecimal(t language.Tag) {
	f.init(t, tagToDecimal)
}

// InitScientific initializes a Formatter using the default Pattern for the
// given language.
func (f *Formatter) InitScientific(t language.Tag) {
	f.init(t, tagToScientific)
}

// InitEngineering initializes a Formatter using the default Pattern for the
// given language.
func (f *Formatter) InitEngineering(t language.Tag) {
	f.init(t, tagToScientific)
	f.Pattern.MaxIntegerDigits = 3
	f.Pattern.MinIntegerDigits = 1
}

// InitPercent initializes a Formatter using the default Pattern for the given
// language.
func (f *Formatter) InitPercent(t language.Tag) {
	f.init(t, tagToPercent)
}

// InitPerMille initializes a Formatter using the default Pattern for the given
// language.
func (f *Formatter) InitPerMille(t language.Tag) {
	f.init(t, tagToPercent)
	f.Pattern.DigitShift = 3
}

func (f *Formatter) Append(dst []byte, x interface{}) []byte {
	var d Decimal
	d.Convert(&f.RoundingContext, x)
	return f.Format(dst, &d)
}

func (f *Formatter) Format(dst []byte, d *Decimal) []byte {
	var result []byte
	var postPrefix, preSuffix int
	if f.MinExponentDigits > 0 {
		result, postPrefix, preSuffix = appendScientific(dst, f, d)
	} else {
		result, postPrefix, preSuffix = appendDecimal(dst, f, d)
	}
	if f.PadRune == 0 {
		return result
	}
	width := int(f.FormatWidth)
	if count := utf8.RuneCount(result); count < width {
		insertPos := 0
		switch f.Flags & PadMask {
		case PadAfterPrefix:
			insertPos = postPrefix
		case PadBeforeSuffix:
			insertPos = preSuffix
		case PadAfterSuffix:
			insertPos = len(result)
		}
		num := width - count
		pad := [utf8.UTFMax]byte{' '}
		sz := 1
		if r := f.PadRune; r != 0 {
			sz = utf8.EncodeRune(pad[:], r)
		}
		extra := sz * num
		if n := len(result) + extra; n < cap(result) {
			result = result[:n]
			copy(result[insertPos+extra:], result[insertPos:])
		} else {
			buf := make([]byte, n)
			copy(buf, result[:insertPos])
			copy(buf[insertPos+extra:], result[insertPos:])
			result = buf
		}
		for ; num > 0; num-- {
			insertPos += copy(result[insertPos:], pad[:sz])
		}
	}
	return result
}

// TODO: just return visible digits.
func decimalVisibleDigits(f *Formatter, d *Decimal) Decimal {
	if d.NaN || d.Inf {
		return *d
	}
	n := d.normalize()
	if maxSig := int(f.MaxSignificantDigits); maxSig > 0 {
		// TODO: really round to zero?
		n.round(ToZero, maxSig)
	}
	digits := n.Digits
	exp := n.Exp
	exp += int32(f.Pattern.DigitShift)

	// Cap integer digits. Remove *most-significant* digits.
	if f.MaxIntegerDigits > 0 {
		if p := int(exp) - int(f.MaxIntegerDigits); p > 0 {
			if p > len(digits) {
				p = len(digits)
			}
			if digits = digits[p:]; len(digits) == 0 {
				exp = 0
			} else {
				exp -= int32(p)
			}
			// Strip leading zeros.
			for len(digits) > 0 && digits[0] == 0 {
				digits = digits[1:]
				exp--
			}
		}
	}

	// Rounding usually is done by convert, but we don't rely on it.
	numFrac := len(digits) - int(exp)
	if f.MaxSignificantDigits == 0 && int(f.MaxFractionDigits) < numFrac {
		p := int(exp) + int(f.MaxFractionDigits)
		if p <= 0 {
			p = 0
		} else if p >= len(digits) {
			p = len(digits)
		}
		digits = digits[:p] // TODO: round
	}

	// set End (trailing zeros)
	n.End = int32(len(digits))
	if len(digits) == 0 {
		if f.MinFractionDigits > 0 {
			n.End = int32(f.MinFractionDigits)
		}
		if p := int32(f.MinSignificantDigits) - 1; p > n.End {
			n.End = p
		}
	} else {
		if end := exp + int32(f.MinFractionDigits); end > n.End {
			n.End = end
		}
		if n.End < int32(f.MinSignificantDigits) {
			n.End = int32(f.MinSignificantDigits)
		}
	}
	n.Digits = digits
	n.Exp = exp
	return n
}

// appendDecimal appends a formatted number to dst. It returns two possible
// insertion points for padding.
func appendDecimal(dst []byte, f *Formatter, d *Decimal) (b []byte, postPre, preSuf int) {
	if dst, ok := f.renderSpecial(dst, d); ok {
		return dst, 0, len(dst)
	}
	n := decimalVisibleDigits(f, d)
	digits := n.Digits
	exp := n.Exp

	// Split in integer and fraction part.
	var intDigits, fracDigits []byte
	numInt := 0
	numFrac := int(n.End - n.Exp)
	if exp > 0 {
		numInt = int(exp)
		if int(exp) >= len(digits) { // ddddd | ddddd00
			intDigits = digits
		} else { // ddd.dd
			intDigits = digits[:exp]
			fracDigits = digits[exp:]
		}
	} else {
		fracDigits = digits
	}

	neg := d.Neg
	affix, suffix := f.getAffixes(neg)
	dst = appendAffix(dst, f, affix, neg)
	savedLen := len(dst)

	minInt := int(f.MinIntegerDigits)
	if minInt == 0 && f.MinSignificantDigits > 0 {
		minInt = 1
	}
	// add leading zeros
	for i := minInt; i > numInt; i-- {
		dst = f.AppendDigit(dst, 0)
		if f.needsSep(i) {
			dst = append(dst, f.Symbol(SymGroup)...)
		}
	}
	i := 0
	for ; i < len(intDigits); i++ {
		dst = f.AppendDigit(dst, intDigits[i])
		if f.needsSep(numInt - i) {
			dst = append(dst, f.Symbol(SymGroup)...)
		}
	}
	for ; i < numInt; i++ {
		dst = f.AppendDigit(dst, 0)
		if f.needsSep(numInt - i) {
			dst = append(dst, f.Symbol(SymGroup)...)
		}
	}

	if numFrac > 0 || f.Flags&AlwaysDecimalSeparator != 0 {
		dst = append(dst, f.Symbol(SymDecimal)...)
	}
	// Add leading zeros
	i = 0
	for n := -int(n.Exp); i < n; i++ {
		dst = f.AppendDigit(dst, 0)
	}
	for _, d := range fracDigits {
		i++
		dst = f.AppendDigit(dst, d)
	}
	for ; i < numFrac; i++ {
		dst = f.AppendDigit(dst, 0)
	}
	return appendAffix(dst, f, suffix, neg), savedLen, len(dst)
}

func scientificVisibleDigits(f *Formatter, d *Decimal) Decimal {
	if d.NaN || d.Inf {
		return *d
	}
	n := d.normalize()

	// Significant digits are transformed by the parser for scientific notation
	// and do not need to be handled here.
	maxInt, numInt := int(f.MaxIntegerDigits), int(f.MinIntegerDigits)
	if numInt == 0 {
		numInt = 1
	}
	maxSig := int(f.MaxFractionDigits) + numInt
	minSig := int(f.MinFractionDigits) + numInt

	if maxSig > 0 {
		// TODO: really round to zero?
		n.round(ToZero, maxSig)
	}
	digits := n.Digits
	exp := n.Exp

	// If a maximum number of integers is specified, the minimum must be 1
	// and the exponent is grouped by this number (e.g. for engineering)
	if len(digits) == 0 {
		exp = 0
	} else if maxInt > numInt {
		// Correct the exponent to reflect a single integer digit.
		exp--
		numInt = 1
		// engineering
		// 0.01234 ([12345]e-1) -> 1.2345e-2  12.345e-3
		// 12345   ([12345]e+5) -> 1.2345e4  12.345e3
		d := int(exp) % maxInt
		if d < 0 {
			d += maxInt
		}
		exp -= int32(d)
		numInt += d
	} else {
		exp -= int32(numInt)
	}

	n.Comma = uint8(numInt)
	n.End = int32(len(digits))
	if n.End < int32(minSig) {
		n.End = int32(minSig)
	}
	n.Digits = digits
	n.Exp = exp
	return n
}

// appendScientific appends a formatted number to dst. It returns two possible
// insertion points for padding.
func appendScientific(dst []byte, f *Formatter, d *Decimal) (b []byte, postPre, preSuf int) {
	if dst, ok := f.renderSpecial(dst, d); ok {
		return dst, 0, 0
	}
	// n := d.normalize()
	n := scientificVisibleDigits(f, d)
	digits := n.Digits
	exp := n.Exp
	numInt := int(n.Comma)
	numFrac := int(n.End) - int(n.Comma)

	var intDigits, fracDigits []byte
	if numInt <= len(digits) {
		intDigits = digits[:numInt]
		fracDigits = digits[numInt:]
	} else {
		intDigits = digits
	}
	neg := d.Neg
	affix, suffix := f.getAffixes(neg)
	dst = appendAffix(dst, f, affix, neg)
	savedLen := len(dst)

	i := 0
	for ; i < len(intDigits); i++ {
		dst = f.AppendDigit(dst, intDigits[i])
		if f.needsSep(numInt - i) {
			dst = append(dst, f.Symbol(SymGroup)...)
		}
	}
	for ; i < numInt; i++ {
		dst = f.AppendDigit(dst, 0)
		if f.needsSep(numInt - i) {
			dst = append(dst, f.Symbol(SymGroup)...)
		}
	}

	if numFrac > 0 || f.Flags&AlwaysDecimalSeparator != 0 {
		dst = append(dst, f.Symbol(SymDecimal)...)
	}
	i = 0
	for ; i < len(fracDigits); i++ {
		dst = f.AppendDigit(dst, fracDigits[i])
	}
	for ; i < numFrac; i++ {
		dst = f.AppendDigit(dst, 0)
	}

	// exp
	buf := [12]byte{}
	// TODO: use exponential if superscripting is not available (no Latin
	// numbers or no tags) and use exponential in all other cases.
	exponential := f.Symbol(SymExponential)
	if exponential == "E" {
		dst = append(dst, "\u202f"...) // NARROW NO-BREAK SPACE
		dst = append(dst, f.Symbol(SymSuperscriptingExponent)...)
		dst = append(dst, "\u202f"...) // NARROW NO-BREAK SPACE
		dst = f.AppendDigit(dst, 1)
		dst = f.AppendDigit(dst, 0)
		switch {
		case exp < 0:
			dst = append(dst, superMinus...)
			exp = -exp
		case f.Flags&AlwaysExpSign != 0:
			dst = append(dst, superPlus...)
		}
		b = strconv.AppendUint(buf[:0], uint64(exp), 10)
		for i := len(b); i < int(f.MinExponentDigits); i++ {
			dst = append(dst, superDigits[0]...)
		}
		for _, c := range b {
			dst = append(dst, superDigits[c-'0']...)
		}
	} else {
		dst = append(dst, exponential...)
		switch {
		case exp < 0:
			dst = append(dst, f.Symbol(SymMinusSign)...)
			exp = -exp
		case f.Flags&AlwaysExpSign != 0:
			dst = append(dst, f.Symbol(SymPlusSign)...)
		}
		b = strconv.AppendUint(buf[:0], uint64(exp), 10)
		for i := len(b); i < int(f.MinExponentDigits); i++ {
			dst = f.AppendDigit(dst, 0)
		}
		for _, c := range b {
			dst = f.AppendDigit(dst, c-'0')
		}
	}
	return appendAffix(dst, f, suffix, neg), savedLen, len(dst)
}

const (
	superMinus = "\u207B" // SUPERSCRIPT HYPHEN-MINUS
	superPlus  = "\u207A" // SUPERSCRIPT PLUS SIGN
)

var (
	// Note: the digits are not sequential!!!
	superDigits = []string{
		"\u2070", // SUPERSCRIPT DIGIT ZERO
		"\u00B9", // SUPERSCRIPT DIGIT ONE
		"\u00B2", // SUPERSCRIPT DIGIT TWO
		"\u00B3", // SUPERSCRIPT DIGIT THREE
		"\u2074", // SUPERSCRIPT DIGIT FOUR
		"\u2075", // SUPERSCRIPT DIGIT FIVE
		"\u2076", // SUPERSCRIPT DIGIT SIX
		"\u2077", // SUPERSCRIPT DIGIT SEVEN
		"\u2078", // SUPERSCRIPT DIGIT EIGHT
		"\u2079", // SUPERSCRIPT DIGIT NINE
	}
)

func (f *Formatter) getAffixes(neg bool) (affix, suffix string) {
	str := f.Affix
	if str != "" {
		if f.NegOffset > 0 {
			if neg {
				str = str[f.NegOffset:]
			} else {
				str = str[:f.NegOffset]
			}
		}
		sufStart := 1 + str[0]
		affix = str[1:sufStart]
		suffix = str[sufStart+1:]
	}
	// TODO: introduce a NeedNeg sign to indicate if the left pattern already
	// has a sign marked?
	if f.NegOffset == 0 && (neg || f.Flags&AlwaysSign != 0) {
		affix = "-" + affix
	}
	return affix, suffix
}

func (f *Formatter) renderSpecial(dst []byte, d *Decimal) (b []byte, ok bool) {
	if d.NaN {
		return fmtNaN(dst, f), true
	}
	if d.Inf {
		return fmtInfinite(dst, f, d), true
	}
	return dst, false
}

func fmtNaN(dst []byte, f *Formatter) []byte {
	return append(dst, f.Symbol(SymNan)...)
}

func fmtInfinite(dst []byte, f *Formatter, d *Decimal) []byte {
	affix, suffix := f.getAffixes(d.Neg)
	dst = appendAffix(dst, f, affix, d.Neg)
	dst = append(dst, f.Symbol(SymInfinity)...)
	dst = appendAffix(dst, f, suffix, d.Neg)
	return dst
}

func appendAffix(dst []byte, f *Formatter, affix string, neg bool) []byte {
	quoting := false
	escaping := false
	for _, r := range affix {
		switch {
		case escaping:
			// escaping occurs both inside and outside of quotes
			dst = append(dst, string(r)...)
			escaping = false
		case r == '\\':
			escaping = true
		case r == '\'':
			quoting = !quoting
		case quoting:
			dst = append(dst, string(r)...)
		case r == '%':
			if f.DigitShift == 3 {
				dst = append(dst, f.Symbol(SymPerMille)...)
			} else {
				dst = append(dst, f.Symbol(SymPercentSign)...)
			}
		case r == '-' || r == '+':
			if neg {
				dst = append(dst, f.Symbol(SymMinusSign)...)
			} else if f.Flags&ElideSign == 0 {
				dst = append(dst, f.Symbol(SymPlusSign)...)
			} else {
				dst = append(dst, ' ')
			}
		default:
			dst = append(dst, string(r)...)
		}
	}
	return dst
}
