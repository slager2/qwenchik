import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Root, RootContent, Parent, Text } from 'mdast'
import type { CitationReference } from './conversation-state.mjs'
import { safeURL } from './conversation-state.mjs'

function evidenceLinks(options: { references: CitationReference[] }) {
  const known = new Map(options.references.map((item) => [item.id, item]))
  const numbers = new Map(options.references.map((item) => [item.label, item]))
  return (tree: Root) => {
    function walk(parent: Parent) {
      const children: RootContent[] = []
      for (const node of parent.children) {
        if (node.type === 'text') {
          const text = (node as Text).value
          let offset = 0
          for (const match of text.matchAll(/\[([^\]\n]+)\]/g)) {
            const reference = known.get(match[1])
            if (!reference || !safeURL(reference.url)) continue
            children.push({ type: 'text', value: text.slice(offset, match.index) })
            children.push({ type: 'link', url: reference.url, title: reference.title, children: [{ type: 'text', value: `[${reference.label}]` }] })
            offset = match.index + match[0].length
          }
          children.push({ type: 'text', value: text.slice(offset) })
        } else if (node.type === 'link') {
          const label = node.children.map((child) => child.type === 'text' ? child.value : '').join('')
          const marker = label.replace(/^\[|\]$/g, '')
          const reference = known.get(marker) || numbers.get(marker)
          if (reference && safeURL(reference.url)) children.push({ ...node, url: reference.url, title: reference.title, children: [{ type: 'text', value: `[${reference.label}]` }] })
          else if (/^\d+$/.test(marker) || /^(?:ev[_-].+|[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f-]{27,})$/i.test(marker)) children.push({ type: 'text', value: `[${marker}]` })
          else children.push(node)
        } else {
          if ('children' in node && node.type !== 'linkReference') walk(node as Parent)
          children.push(node as RootContent)
        }
      }
      parent.children = children as Parent['children']
    }
    walk(tree)
  }
}

export default function MarkdownMessage({ content, references = [] }: { content: string; references?: CitationReference[] }) {
  const safeReferences = references.filter((reference) => safeURL(reference.url))
  return <div className="chat-markdown"><ReactMarkdown skipHtml remarkPlugins={[remarkGfm, [evidenceLinks, { references: safeReferences }]]} urlTransform={(url) => safeURL(url)} components={{
    a: ({ href, children, title }) => href ? <a href={href} title={title} target="_blank" rel="noopener noreferrer">{children}</a> : <span>{children}</span>,
    img: ({ alt }) => <span className="chat-image-label">{alt || 'Изображение'}</span>,
    table: ({ children }) => <div className="chat-table-scroll" tabIndex={0} role="region" aria-label="Таблица"><table>{children}</table></div>,
  }}>{content}</ReactMarkdown>{safeReferences.length > 0 && <details className="chat-citation-sources"><summary>Источники · {safeReferences.length}</summary><ol>{safeReferences.map((reference) => {
    const url = safeURL(reference.url)
    const hostname = new URL(url).hostname
    return <li key={reference.id}><span className="chat-citation-number">[{reference.label}]</span><a href={url} target="_blank" rel="noopener noreferrer">{reference.title || hostname}</a><small>{hostname}</small></li>
  })}</ol><p>Ссылка показывает источник материала, но не подтверждает автоматически каждое утверждение.</p></details>}</div>
}
