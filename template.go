package vue

import (
	"bytes"
	"fmt"
	"github.com/cbroglie/mustache"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"io"
	"reflect"
	"strings"
)

const (
	v      = "v-"
	vBind  = "v-bind"
	vFor   = "v-for"
	vIf    = "v-if"
	vModel = "v-model"
	vOn    = "v-on"
)

var attrOrder = []string{vFor, vIf, vModel, vOn, vBind}

type template struct {
	comp *Comp

	id   int64
	flag *html.Node
}

// newTemplate creates a new template.
func newTemplate(comp *Comp) *template {
	return &template{comp: comp, flag: &html.Node{}}
}

// execute executes the template with the given data to be rendered.
func (tmpl *template) execute(data map[string]interface{}) []byte {
	buf := bytes.NewBuffer(tmpl.comp.tmpl)
	nodes := parse(buf)
	if n := len(nodes); n != 1 {
		must(fmt.Errorf("expected a single root element for template: %s but found: %d",
			tmpl.comp.tmpl, n))
	}

	node := tmpl.executeTraversal(nodes[0], data)

	buf = bytes.NewBuffer(nil)
	err := html.Render(buf, node)
	must(err)

	template, err := mustache.ParseString(buf.String())
	must(err)

	buf.Reset()
	err = template.FRender(buf, data)
	must(err)

	return buf.Bytes()
}

// executeTraversal recursively traverses the html tree and templates the elements.
func (tmpl *template) executeTraversal(node *html.Node, data map[string]interface{}) *html.Node {
	// Leave the text nodes to be rendered.
	if node.Type != html.ElementNode {
		return node
	}

	// Attempt to create a subcomponent from the element.
	sub, ok := tmpl.comp.newSub(node.Data)

	// Order attributes before execution.
	orderAttrs(node)

	// Execute attributes.
	for i := 0; i < len(node.Attr); i++ {
		attr := node.Attr[i]
		if strings.HasPrefix(attr.Key, v) {
			deleteAttr(node, i)
			i--
			node = tmpl.executeAttr(node, sub, attr, data)
			// The flag signals that the tree structure was modified.
			// The next sibling of flag is the node to execute next.
			if node == tmpl.flag {
				return node
			}
		}
	}

	// Execute subcomponent.
	if ok {
		vm := newViewModel(sub)
		subNode := vm.subRender()
		node.Parent.InsertBefore(subNode, node)
		node.Parent.RemoveChild(node)
		// No need to use flag since the subcomponent node is already executed.
		return subNode
	}

	// Execute children.
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		child = tmpl.executeTraversal(child, data)
	}
	// The flag must be removed if used, which preserves the expected html structure.
	// The flag node intentionally fails to execute.
	if node == tmpl.flag.Parent {
		node.RemoveChild(tmpl.flag)
	}

	return node
}

// executeAttr executes the given vue attribute.
func (tmpl *template) executeAttr(node *html.Node, sub *Comp, attr html.Attribute, data map[string]interface{}) *html.Node {
	vals := strings.Split(attr.Key, ":")
	dir, part := vals[0], ""
	if len(vals) > 1 {
		part = vals[1]
	}
	switch dir {
	case vIf:
		node = tmpl.executeAttrIf(node, attr.Val, data)
	case vFor:
		node = tmpl.executeAttrFor(node, attr.Val, data)
	case vBind:
		// break
		executeAttrBind(node, sub, part, attr.Val, data)
	case vModel:
		tmpl.executeAttrModel(node, attr.Val, data)
	case vOn:
		tmpl.executeAttrOn(node, part, attr.Val)
	default:
		must(fmt.Errorf("unknown vue attribute: %v", dir))
	}
	return node
}

// executeAttrIf executes the vue if attribute.
func (tmpl *template) executeAttrIf(node *html.Node, field string, data map[string]interface{}) *html.Node {
	if value, ok := data[field]; ok {
		if val, ok := value.(bool); ok && val {
			return node
		}
	}
	node.Parent.InsertBefore(tmpl.flag, node)
	node.Parent.RemoveChild(node)
	return tmpl.flag
}

// executeAttrFor executes the vue for attribute.
func (tmpl *template) executeAttrFor(node *html.Node, value string, data map[string]interface{}) *html.Node {
	vals := strings.Split(value, "in")
	name := bytes.TrimSpace([]byte(vals[0]))
	field := strings.TrimSpace(vals[1])

	slice, ok := data[field]
	if !ok {
		must(fmt.Errorf("slice not found for field: %s", field))
	}

	elem := bytes.NewBuffer(nil)
	err := html.Render(elem, node)
	must(err)

	buf := bytes.NewBuffer(nil)
	values := reflect.ValueOf(slice)
	n := values.Len()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s%d", name, tmpl.id)
		tmpl.id++

		b := bytes.Replace(elem.Bytes(), name, []byte(key), -1)
		_, err := buf.Write(b)
		must(err)

		data[key] = values.Index(i).Interface()
	}

	nodes := parse(buf)
	node.Parent.InsertBefore(tmpl.flag, node)
	for _, child := range nodes {
		node.Parent.InsertBefore(child, node)
	}
	node.Parent.RemoveChild(node)

	return tmpl.flag
}

// executeAttrBind executes the vue bind attribute.
func executeAttrBind(node *html.Node, sub *Comp, key, value string, data map[string]interface{}) {
	field, ok := data[value]
	if !ok {
		must(fmt.Errorf("unknown data field: %s", value))
	}

	prop := strings.Title(key)
	if sub.hasProp(prop) {
		sub.props[prop] = field
		return
	}

	// Remove attribute if bound to a false value of type bool.
	if val, ok := field.(bool); ok && !val {
		return
	}

	node.Attr = append(node.Attr, html.Attribute{Key: key, Val: fmt.Sprintf("%v", field)})
}

// executeAttrModel executes the vue model attribute.
func (tmpl *template) executeAttrModel(node *html.Node, field string, data map[string]interface{}) {
	typ := "input"
	node.Attr = append(node.Attr, html.Attribute{Key: typ, Val: field})
	tmpl.comp.callback.addEventListener(typ, tmpl.comp.callback.vModel)

	value, ok := data[field]
	if !ok {
		must(fmt.Errorf("unknown data field: %s", field))
	}
	val, ok := value.(string)
	if !ok {
		must(fmt.Errorf("data field is not of type string: %T", field))
	}
	node.Attr = append(node.Attr, html.Attribute{Key: "value", Val: val})
}

// executeAttrOn executes the vue on attribute.
func (tmpl *template) executeAttrOn(node *html.Node, typ, method string) {
	node.Attr = append(node.Attr, html.Attribute{Key: typ, Val: method})
	tmpl.comp.callback.addEventListener(typ, tmpl.comp.callback.vOn)
}

// parse parses the template into html nodes.
func parse(reader io.Reader) []*html.Node {
	nodes, err := html.ParseFragment(reader, &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	must(err)
	return nodes
}

// orderAttrs orders the attributes of the node which orders the template execution.
func orderAttrs(node *html.Node) {
	n := len(node.Attr)
	if n == 0 {
		return
	}
	attrs := make([]html.Attribute, 0, n)
	for _, prefix := range attrOrder {
		for _, attr := range node.Attr {
			if strings.HasPrefix(attr.Key, prefix) {
				attrs = append(attrs, attr)
			}
		}
	}
	// Append other attributes which are not vue attributes.
	for _, attr := range node.Attr {
		if !strings.HasPrefix(attr.Key, v) {
			attrs = append(attrs, attr)
		}
	}
	node.Attr = attrs
}

// deleteAttr deletes the attribute of the node at the index.
// Attribute order is preserved.
func deleteAttr(node *html.Node, i int) {
	node.Attr = append(node.Attr[:i], node.Attr[i+1:]...)
}