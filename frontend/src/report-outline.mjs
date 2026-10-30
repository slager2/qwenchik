import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'

function headingText(node) {
  if (node.type === 'html') return ''
  if (typeof node.value === 'string') return node.value
  if (node.type === 'image') return node.alt || ''
  return (node.children || []).map(headingText).join('')
}

// Position-based IDs also distinguish repeated titles and nested headings.
export function headingID(prefix, offset) {
  return `${prefix}-heading-${offset}`
}

export function reportOutline(content, prefix) {
  const tree = unified().use(remarkParse).use(remarkGfm).parse(content)
  const headings = []
  function visit(node) {
    if (node.type === 'heading') headings.push({ id: headingID(prefix, node.position.start.offset), text: headingText(node).trim() || 'Раздел', depth: node.depth })
    for (const child of node.children || []) visit(child)
  }
  visit(tree)
  return headings
}

export function focusReportHeading(container, id) {
  const heading = Array.from(container?.querySelectorAll('.chat-markdown [id]') || []).find((node) => node.id === id)
  if (!heading) return
  heading.focus({ preventScroll: true })
  heading.scrollIntoView({ block: 'start', behavior: 'instant' })
}
