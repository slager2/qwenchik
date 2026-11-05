import type { HTMLAttributes, ReactNode } from 'react'
import { cx } from './cx'

type CardProps = HTMLAttributes<HTMLElement> & {
  as?: 'div' | 'section' | 'article' | 'aside'
  title?: ReactNode
  actions?: ReactNode
}

export default function Card({ as: Tag = 'div', title, actions, className, children, ...props }: CardProps) {
  return <Tag className={cx('ui-card', className)} {...props}>
    {(title || actions) && <header className="ui-card__header">{title && <h3 className="ui-card__title">{title}</h3>}{actions && <div className="ui-card__actions">{actions}</div>}</header>}
    {children}
  </Tag>
}
