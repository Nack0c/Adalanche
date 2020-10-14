package main

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"

	"github.com/gobwas/glob"
)

type Query interface {
	Evaluate(o *Object) bool
}

type ObjectStrings interface {
	Strings(o *Object) []string
}

type ObjectInt interface {
	Int(o *Object) (int64, bool)
}

type comparatortype byte

const (
	CompareEquals comparatortype = iota
	CompareLessThan
	CompareLessThanEqual
	CompareGreaterThan
	CompareGreaterThanEqual
)

func (c comparatortype) Compare(a, b int64) bool {
	switch c {
	case CompareEquals:
		return a == b
	case CompareLessThan:
		return a < b
	case CompareLessThanEqual:
		return a <= b
	case CompareGreaterThan:
		return a > b
	case CompareGreaterThanEqual:
		return a >= b
	}
	return false // I hope not
}

func (a Attribute) Strings(o *Object) []string {
	return o.AttrRendered(a)
}

func (a Attribute) Ints(o *Object) (int64, bool) {
	return o.AttrInt(a)
}

func ParseQueryStrict(s string) (Query, error) {
	s, query, err := ParseQuery(s)
	if err == nil && s != "" {
		return nil, fmt.Errorf("Extra data after query parsing: %v", s)
	}
	return query, err
}

func ParseQuery(s string) (string, Query, error) {
	if len(s) < 5 {
		return "", nil, errors.New("Query string too short")
	}
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return "", nil, errors.New("Query must start with ( and end with )")
	}
	// Strip (
	s = s[1:]
	var subqueries []Query
	var query Query
	var err error
	switch s[0] {
	case '(': // double wrapped query?
		s, query, err = ParseQuery(s)
		if err != nil {
			return "", nil, err
		}
		// Strip )
		return s[1:], query, nil
	case '&':
		s, subqueries, err = parsemultiplequeries(s[1:])
		if err != nil {
			return "", nil, err
		}
		// Strip )
		return s[1:], andquery{subqueries}, nil
	case '|':
		s, subqueries, err = parsemultiplequeries(s[1:])
		if err != nil {
			return "", nil, err
		}
		// Strip )
		return s[1:], orquery{subqueries}, nil
	case '!':
		s, query, err = ParseQuery(s[1:])
		if err != nil {
			return "", nil, err
		}
		return s[1:], notquery{query}, err
	}

	// parse one Attribute = Value pair
	var modifier string
	var attributename string

	// Attribute name
attributeloop:
	for {
		if len(s) == 0 {
			return "", nil, errors.New("Incompete query attribute name detected")
		}
		switch s[0] {
		case '\\': // Escaping
			attributename += string(s[1])
			s = s[2:] // yum yum
		case ':':
			// Modifier
			nextcolon := strings.Index(s[1:], ":")
			if nextcolon == -1 {
				return "", nil, errors.New("Incompete query string detected (only one colon modifier)")
			}
			modifier = s[1 : nextcolon+1]
			s = s[nextcolon+2:]
			break attributeloop
		case ')':
			return "", nil, errors.New("Unexpected closing parantesis")
		case '~', '=', '<', '>':
			break attributeloop
		default:
			attributename += string(s[0])
			s = s[1:]
		}
	}

	// Comparator
	comparatorstring := string(s[0])
	if s[0] == '~' {
		if s[1] != '=' {
			return "", nil, errors.New("Tilde operator MUST be followed by EQUALS")
		}
		// Microsoft LDAP does not distinguish between ~= and =, so we don't care either
		// https://docs.microsoft.com/en-us/openspecs/windows_protocols/ms-adts/0bb88bda-ed8d-4af7-9f7b-813291772990
		comparatorstring = "="
		s = s[2:]
	} else if (s[0] == '<' || s[0] == '>') && (s[1] == '=') {
		comparatorstring += "="
		s = s[2:]
	} else {
		s = s[1:]
	}

	comparator := CompareEquals
	switch comparatorstring {
	case "<":
		comparator = CompareLessThan
	case "<=":
		comparator = CompareLessThanEqual
	case ">":
		comparator = CompareGreaterThan
	case ">=":
		comparator = CompareGreaterThanEqual
	}

	// Value
	var value string
valueloop:
	for {
		if len(s) == 0 {
			return "", nil, errors.New("Incomplete query value detected")
		}
		switch s[0] {
		case '\\': // Escaping
			value += string(s[1])
			s = s[2:] // yum yum
		case ')':
			break valueloop
		default:
			value += string(s[0])
			s = s[1:]
		}
	}

	// Eat the )
	s = s[1:]

	valuenum, numok := strconv.ParseInt(value, 10, 64)

	if attributename[0] == '_' {
		// Magic attributes, uuuuuh ....
		switch attributename {
		case "_limit":
			if numok != nil {
				return "", nil, errors.New("Could not convert value to integer for limit limiter")
			}
			return s, &limit{valuenum}, nil
		case "_random100":
			if numok != nil {
				return "", nil, errors.New("Could not convert value to integer for random100 limiter")
			}
			return s, &random100{comparator, valuenum}, nil
		case "_pwnable", "_canpwn":
			pwnmethod := value
			var target Query
			if strings.Contains(pwnmethod, ",") {
				values := strings.Split(pwnmethod, ",")
				pwnmethod = values[0]
				target, _ = ParseQueryStrict(values[1])
			}
			var method PwnMethod
			if pwnmethod != "" && pwnmethod != "*" {
				if method, err = PwnMethodString(pwnmethod); err != nil {
					return "", nil, fmt.Errorf("Could not convert value %v to pwn method", pwnmethod)
				}
			}
			return s, pwnquery{attributename == "_canpwn", method, target}, nil
		default:
			return "", nil, fmt.Errorf("Unknown synthetic attribute %v", attributename)
		}
	}

	attribute := A(attributename)
	var casesensitive bool

	// Decide what to do
	switch modifier {
	case "":
		// That's OK, this is default :-)
	case "caseExactMatch":
		casesensitive = true
	case "count":
		if numok != nil {
			return "", nil, errors.New("Could not convert value to integer for modifier comparison")
		}
		return s, countModifier{attribute, comparator, valuenum}, nil
	case "len", "length":
		if numok != nil {
			return "", nil, errors.New("Could not convert value to integer for modifier comparison")
		}
		return s, lengthModifier{attribute, comparator, valuenum}, nil
	case "1.2.840.113556.1.4.803", "and":
		if comparator != CompareEquals {
			return "", nil, errors.New("Modifier 1.2.840.113556.1.4.803 requires equality comparator")
		}
		return s, andModifier{attribute, valuenum}, nil
	case "1.2.840.113556.1.4.804", "or":
		if comparator != CompareEquals {
			return "", nil, errors.New("Modifier 1.2.840.113556.1.4.804 requires equality comparator")
		}
		return s, orModifier{attribute, valuenum}, nil
	case "1.2.840.113556.1.4.1941", "dnchain":
		// Matching rule in chain
		return s, recursiveDNmatcher{attribute, value}, nil
	default:
		return "", nil, errors.New("Unknown modifier " + modifier)
	}

	// string comparison
	if comparator == CompareEquals {
		if value == "*" {
			return s, hasAttr(attribute), nil
		}
		if strings.HasPrefix(value, "/") && strings.HasSuffix(value, "/") {
			// regexp magic
			pattern := value[1 : len(value)-1]
			if !casesensitive {
				pattern = strings.ToLower(pattern)
			}
			r, err := regexp.Compile(pattern)
			if err != nil {
				return "", nil, err
			}
			return s, hasRegexpMatch{attribute, r}, nil
		}
		if strings.ContainsAny(value, "?*") {
			// glob magic
			pattern := value
			if !casesensitive {
				pattern = strings.ToLower(pattern)
			}
			g, err := glob.Compile(pattern)
			if err != nil {
				return "", nil, err
			}
			return s, hasGlobMatch{attribute, g, casesensitive}, nil
		}
		if casesensitive {
			return s, hasStringMatch{attribute, value}, nil
		}
		return s, hasInsensitiveStringMatch{attribute, strings.ToLower(value)}, nil
	}

	// the other comparators require numeric value
	if numok != nil {
		return "", nil, errors.New("Could not convert value to integer for numeric comparison")
	}

	return s, numericComparator{attribute, comparator, valuenum}, nil
}

func parsemultiplequeries(s string) (string, []Query, error) {
	var result []Query
	for len(s) > 0 && s[0] == '(' {
		var query Query
		var err error
		s, query, err = ParseQuery(s)
		if err != nil {
			return s, nil, err
		}
		result = append(result, query)
	}
	if len(s) == 0 || s[0] != ')' {
		return "", nil, fmt.Errorf("Expecting ) at end of group of queries, but had '%v'", s)
	}
	return s, result, nil
}

type andquery struct {
	subitems []Query
}

func (q andquery) Evaluate(o *Object) bool {
	for _, query := range q.subitems {
		if !query.Evaluate(o) {
			return false
		}
	}
	return true
}

type orquery struct {
	subitems []Query
}

func (q orquery) Evaluate(o *Object) bool {
	for _, query := range q.subitems {
		if query.Evaluate(o) {
			return true
		}
	}
	return false
}

type notquery struct {
	subitem Query
}

func (q notquery) Evaluate(o *Object) bool {
	return !q.subitem.Evaluate(o)
}

type countModifier struct {
	a     ObjectStrings
	c     comparatortype
	value int64
}

func (a countModifier) Evaluate(o *Object) bool {
	return a.c.Compare(int64(len(a.a.Strings(o))), a.value)
}

type lengthModifier struct {
	a     ObjectStrings
	c     comparatortype
	value int64
}

func (a lengthModifier) Evaluate(o *Object) bool {
	for _, value := range a.a.Strings(o) {
		if a.c.Compare(int64(len(value)), a.value) {
			return true
		}
	}
	return false
}

type andModifier struct {
	a     Attribute
	value int64
}

func (a andModifier) Evaluate(o *Object) bool {
	val, ok := o.AttrInt(a.a)
	if !ok {
		return false
	}
	return (int64(val) & a.value) == a.value
}

type orModifier struct {
	a     Attribute
	value int64
}

func (om orModifier) Evaluate(o *Object) bool {
	val, ok := o.AttrInt(om.a)
	if !ok {
		return false
	}
	return int64(val)&om.value != 0
}

type numericComparator struct {
	a     Attribute
	c     comparatortype
	value int64
}

func (nc numericComparator) Evaluate(o *Object) bool {
	val, _ := o.AttrInt(nc.a)
	// if !ok {
	// 	return false
	// }
	return nc.c.Compare(val, nc.value)
}

type limit struct {
	counter int64
}

func (l *limit) Evaluate(o *Object) bool {
	l.counter--
	return l.counter >= 0
}

type random100 struct {
	c comparatortype
	v int64
}

func (r random100) Evaluate(o *Object) bool {
	rnd := rand.Int63n(100)
	return r.c.Compare(rnd, r.v)
}

type hasAttr Attribute

func (a hasAttr) Evaluate(o *Object) bool {
	return len(Attribute(a).Strings(o)) > 0
}

type hasStringMatch struct {
	a ObjectStrings
	m string
}

func (a hasStringMatch) Evaluate(o *Object) bool {
	for _, value := range a.a.Strings(o) {
		if a.m == value {
			return true
		}
	}
	return false
}

// Need you to lowercase m when creating it!!
type hasInsensitiveStringMatch struct {
	a Attribute
	m string
}

func (a hasInsensitiveStringMatch) Evaluate(o *Object) bool {
	for _, value := range o.AttrRendered(a.a) {
		if a.m == strings.ToLower(value) {
			return true
		}
	}
	return false
}

type hasGlobMatch struct {
	a             ObjectStrings
	m             glob.Glob
	casesensitive bool
}

func (a hasGlobMatch) Evaluate(o *Object) bool {
	for _, value := range a.a.Strings(o) {
		if !a.casesensitive {
			value = strings.ToLower(value)
		}
		if a.m.Match(value) {
			return true
		}
	}
	return false
}

type hasRegexpMatch struct {
	a ObjectStrings
	m *regexp.Regexp
}

func (a hasRegexpMatch) Evaluate(o *Object) bool {
	for _, value := range a.a.Strings(o) {
		if a.m.MatchString(value) {
			return true
		}
	}
	return false
}

type recursiveDNmatcher struct {
	a  Attribute
	dn string
}

func (a recursiveDNmatcher) Evaluate(o *Object) bool {
	return recursiveDNmatchFunc(o, a.a, a.dn, 10)
}

func recursiveDNmatchFunc(o *Object, a Attribute, dn string, maxdepth int) bool {
	// Just to prevent loops
	if maxdepth == 0 {
		return false
	}
	// Check all attribute values for match or ancestry
	for _, value := range o.AttrRendered(a) {
		// We're at the end
		if strings.ToLower(value) == strings.ToLower(dn) {
			return true
		}
		// Perhaps parent matches?
		if parent, found := AllObjects.Find(value); found {
			return recursiveDNmatchFunc(parent, a, dn, maxdepth-1)
		}
	}
	return false
}

type pwnquery struct {
	canpwn bool
	method PwnMethod
	target Query
}

func (p pwnquery) Evaluate(o *Object) bool {
	items := o.CanPwn
	if !p.canpwn {
		items = o.PwnableBy
	}
	for _, pwninfo := range items {
		if p.method == 0 || p.method == pwninfo.Method {
			return true
		}
	}
	return false
}

type pwnable PwnMethod

func (p pwnable) Evaluate(o *Object) bool {
	for _, pwninfo := range o.PwnableBy {
		if pwninfo.Method == PwnMethod(p) {
			return true
		}
	}
	return false
}
