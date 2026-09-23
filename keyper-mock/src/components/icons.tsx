// Line icons from the Claude Design canvas. One component per shape; size and
// stroke follow the place they are used in the design.

type IconProps = { size?: number; stroke?: string; width?: number }

function Svg({ size = 15, stroke = 'currentColor', width = 1.9, children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke={stroke} strokeWidth={width}>
      {children}
    </svg>
  )
}

export const HouseMark = () => (
  <Svg size={16} stroke="#fff" width={2}>
    <path d="M4 21V7l8-4 8 4v14" />
    <path d="M9 21v-6h6v6" />
  </Svg>
)

export const DocIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
    <path d="M14 3v5h5" />
  </Svg>
)

export const HistoryIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 12a9 9 0 1 0 3-6.7L3 8" />
    <path d="M3 4v4h4M12 8v4l3 2" />
  </Svg>
)

export const WrenchIcon = (p: IconProps) => (
  <Svg size={16} {...p}>
    <path d="M14.7 6.3a4 4 0 0 1-5 5L4 17v3h3l5.7-5.7a4 4 0 0 0 5-5z" />
  </Svg>
)

export const BookIcon = (p: IconProps) => (
  <Svg size={16} {...p}>
    <path d="M4 5a2 2 0 0 1 2-2h6v18H6a2 2 0 0 1-2-2z" />
    <path d="M20 5a2 2 0 0 0-2-2h-6v18h6a2 2 0 0 0 2-2z" />
  </Svg>
)

export const PlugIcon = (p: IconProps) => (
  <Svg size={16} {...p}>
    <path d="M9 3v6M15 3v6" />
    <path d="M6 9h12v4a6 6 0 0 1-12 0z" />
    <path d="M12 19v2" />
  </Svg>
)

export const ArrowRight = (p: IconProps) => (
  <Svg width={2.2} {...p}>
    <path d="M5 12h13M13 6l6 6-6 6" />
  </Svg>
)

export const ArrowLeft = (p: IconProps) => (
  <Svg width={2.2} {...p}>
    <path d="M19 12H6M11 6l-6 6 6 6" />
  </Svg>
)

export const SaveIcon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
    <path d="M17 21v-8H7v8M7 3v5h8" />
  </Svg>
)

export const InfoIcon = () => (
  <Svg size={17} width={2}>
    <circle cx="12" cy="12" r="10" />
    <path d="M12 16v-4M12 8h.01" />
  </Svg>
)

export const PersonIcon = () => (
  <Svg size={17}>
    <circle cx="12" cy="8" r="3.5" />
    <path d="M5 20c0-3.6 3.1-6 7-6s7 2.4 7 6" />
  </Svg>
)

export const SearchIcon = () => (
  <Svg width={2}>
    <circle cx="11" cy="11" r="7" />
    <path d="M20 20l-3.5-3.5" />
  </Svg>
)

export const TrashIcon = () => (
  <Svg size={14}>
    <path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3" />
  </Svg>
)

export const ClockIcon = () => (
  <Svg size={11} width={2.6}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5l3 2" />
  </Svg>
)

export const CheckIcon = ({ size = 14 }: IconProps) => (
  <Svg size={size} width={2.6}>
    <path d="M5 12l5 5L20 7" />
  </Svg>
)

export const ShieldIcon = (p: IconProps) => (
  <Svg size={18} {...p}>
    <path d="M12 3l8 3v6c0 5-3.4 8.4-8 9-4.6-.6-8-4-8-9V6z" />
  </Svg>
)

export const ChevronRight = () => (
  <Svg size={14} width={2.2}>
    <path d="M9 6l6 6-6 6" />
  </Svg>
)
