import type { SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement>

function Base({ children, ...props }: IconProps) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...props}
    >
      {children}
    </svg>
  )
}

export function ServerIcon(props: IconProps) {
  return (
    <Base {...props}>
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <path d="M7 7.5h.01M7 16.5h.01" />
    </Base>
  )
}

export function PulseIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3 12h3.5l2-6 3.5 12 2.5-9 1.5 3H21" />
    </Base>
  )
}

export function OfflineIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3 12h4l2 5 4-10 2 5h6" />
    </Base>
  )
}

export function PlusIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M12 5v14M5 12h14" />
    </Base>
  )
}

export function RefreshIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M20 11a8 8 0 1 0-2.34 5.66M20 5v6h-6" />
    </Base>
  )
}

export function CopyIcon(props: IconProps) {
  return (
    <Base {...props}>
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
    </Base>
  )
}

export function CheckIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="m5 13 4 4L19 7" />
    </Base>
  )
}

export function CloseIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M6 6l12 12M18 6L6 18" />
    </Base>
  )
}

export function SunIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41" />
    </Base>
  )
}

export function MoonIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M21 12.8A8.5 8.5 0 1 1 11.2 3a6.5 6.5 0 0 0 9.8 9.8Z" />
    </Base>
  )
}

export function LogoutIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" />
    </Base>
  )
}

export function PencilIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
    </Base>
  )
}

export function TrashIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v5M14 11v5" />
    </Base>
  )
}

export function ArrowLeftIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M19 12H5M12 19l-7-7 7-7" />
    </Base>
  )
}

export function ChevronRightIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="m9 18 6-6-6-6" />
    </Base>
  )
}

export function TerminalIcon(props: IconProps) {
  return (
    <Base {...props}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="m7 9 3 3-3 3M13 15h4" />
    </Base>
  )
}

export function CpuIcon(props: IconProps) {
  return (
    <Base {...props}>
      <rect x="5" y="5" width="14" height="14" rx="2" />
      <rect x="9.5" y="9.5" width="5" height="5" rx="1" />
      <path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3" />
    </Base>
  )
}

export function ClockIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </Base>
  )
}

export function SpinnerIcon({ className, ...props }: IconProps) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      className={`animate-spin ${className ?? ''}`}
      aria-hidden="true"
      {...props}
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.25" strokeWidth="3" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  )
}

export function KeyIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="8" cy="15" r="4" />
      <path d="m10.8 12.2 7.2-7.2M15.5 5.5 18 8M13 8l3 3" />
    </Base>
  )
}

export function TagIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M20.6 13.4 13.4 20.6a2 2 0 0 1-2.8 0l-7.2-7.2a2 2 0 0 1-.6-1.4V5a2 2 0 0 1 2-2h7a2 2 0 0 1 1.4.6l7.4 7.4a2 2 0 0 1 0 2.4Z" />
      <path d="M7.5 7.5h.01" />
    </Base>
  )
}

export function FolderIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
    </Base>
  )
}

export function WifiIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M2.5 9a15 15 0 0 1 19 0M6 12.5a10 10 0 0 1 12 0M9.5 16a5 5 0 0 1 5 0" />
      <path d="M12 19.5h.01" />
    </Base>
  )
}

export function AlertTriangleIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M10.3 3.9 2.5 17.4A2 2 0 0 0 4.2 20.4h15.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" />
      <path d="M12 9v4M12 16.5h.01" />
    </Base>
  )
}

export function ChartIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M4 4v16h16" />
      <path d="m7 14 3.5-4 3 2.5L18 7" />
    </Base>
  )
}

export function InfoIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5M12 7.8h.01" />
    </Base>
  )
}

export function ListChecksIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="m3 6 2 2 3-3M3 14l2 2 3-3M3 20h4" />
      <path d="M13 7h8M13 15h8M13 21h8" />
    </Base>
  )
}

export function PlayIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M7 4.5v15l12-7.5Z" />
    </Base>
  )
}

export function ShieldCheckIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M12 3l7 3v6c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6Z" />
      <path d="m9 12 2 2 4-4" />
    </Base>
  )
}

export function BellIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M18 8a6 6 0 1 0-12 0c0 5-2 6-2 6h16s-2-1-2-6" />
      <path d="M10.3 19a2 2 0 0 0 3.4 0" />
    </Base>
  )
}

export function BellOffIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M8.7 3.9A6 6 0 0 1 18 8c0 2.6.5 4.2 1 5.2M6 8c0 5-2 6-2 6h12" />
      <path d="M10.3 19a2 2 0 0 0 3.4 0M3 3l18 18" />
    </Base>
  )
}

export function SendIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M21.5 3.5 2.8 10.4a.6.6 0 0 0 .05 1.13l7.2 2.4 2.4 7.2a.6.6 0 0 0 1.13.05Z" />
      <path d="M21.5 3.5 10.05 13.95" />
    </Base>
  )
}

export function HistoryIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3.5 12a8.5 8.5 0 1 0 2.5-6" />
      <path d="M3 4v4h4M12 8v4.5l3 1.8" />
    </Base>
  )
}

export function CheckCircleIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="m8.5 12.2 2.4 2.4 4.6-4.8" />
    </Base>
  )
}

export function RotateCcwIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M3.5 5v5h5" />
      <path d="M4.2 10a8 8 0 1 1 1.6 5.6" />
    </Base>
  )
}

export function ArchiveIcon(props: IconProps) {
  return (
    <Base {...props}>
      <rect x="3" y="4" width="18" height="4" rx="1" />
      <path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4" />
    </Base>
  )
}

export function DownloadIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M12 3v12M7 10l5 5 5-5M4 20h16" />
    </Base>
  )
}

export function UploadIcon(props: IconProps) {
  return (
    <Base {...props}>
      <path d="M12 21V9M7 14l5-5 5 5M4 4h16" />
    </Base>
  )
}

export function KeyRoundIcon(props: IconProps) {
  return (
    <Base {...props}>
      <circle cx="8" cy="15" r="4" />
      <path d="m10.8 12.2 8.2-8.2M16.5 4.5 19 7M14 7l2.5 2.5" />
    </Base>
  )
}
