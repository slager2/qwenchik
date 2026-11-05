import { cx } from './cx'

export default function Spinner({ size = 'md', label = 'Загрузка', decorative, className }: { size?: 'sm' | 'md'; label?: string; decorative?: boolean; className?: string }) {
  return <span className={cx('ui-spinner', size === 'sm' && 'ui-spinner--sm', className)} role={decorative ? undefined : 'status'} aria-label={decorative ? undefined : label} aria-hidden={decorative || undefined} />
}
