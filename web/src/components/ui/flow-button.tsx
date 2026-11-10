'use client'

import type { ButtonHTMLAttributes } from 'react'
import { ArrowRight, LoaderCircle } from 'lucide-react'
import { cn } from '@/lib/utils'

type FlowButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  text?: string
  loading?: boolean
}

export function FlowButton({text = 'Modern Button', loading = false, disabled, className = '', type = 'button', ...props}: FlowButtonProps) {
  const arrow = 'absolute z-20 h-4 w-4 fill-none stroke-current transition-all duration-[800ms] ease-[cubic-bezier(0.34,1.56,0.64,1)] motion-reduce:transition-none group-data-[loading=true]:opacity-0'
  return <button {...props} type={type} disabled={disabled || loading} aria-busy={loading} data-loading={loading} className={cn(`group relative isolate flex items-center justify-center gap-1 overflow-hidden rounded-[100px] border-[1.5px] border-solid border-[#333333]/40 bg-transparent px-8 py-3 text-sm font-semibold text-[#111111] cursor-pointer transition-all duration-[600ms] ease-[cubic-bezier(0.23,1,0.32,1)] enabled:hover:border-transparent enabled:hover:text-white enabled:active:scale-[0.95] focus-visible:border-transparent focus-visible:text-white data-[loading=true]:text-white disabled:pointer-events-none disabled:cursor-wait disabled:opacity-70 motion-reduce:transition-none`, className)}>
    <ArrowRight aria-hidden="true" className={`${arrow} left-[-25%] group-hover:left-4 group-focus-visible:left-4`} />
    <span className="relative z-10 -translate-x-3 transition-all duration-[800ms] ease-out group-hover:translate-x-3 group-focus-visible:translate-x-3 group-data-[loading=true]:translate-x-0 motion-reduce:transition-none">{text}</span>
    <span aria-hidden="true" className="absolute top-1/2 left-1/2 z-0 -translate-x-1/2 -translate-y-1/2 h-4 w-4 rounded-[50%] bg-[#111111] opacity-0 transition-all duration-[800ms] ease-[cubic-bezier(0.19,1,0.22,1)] group-hover:h-[420px] group-hover:w-[420px] group-hover:opacity-100 group-focus-visible:h-[420px] group-focus-visible:w-[420px] group-focus-visible:opacity-100 group-data-[loading=true]:h-[420px] group-data-[loading=true]:w-[420px] group-data-[loading=true]:opacity-100 motion-reduce:transition-none" />
    <ArrowRight aria-hidden="true" className={`${arrow} right-4 group-hover:right-[-25%] group-focus-visible:right-[-25%]`} />
    {loading && <LoaderCircle aria-hidden="true" className="relative z-20 h-4 w-4 animate-spin motion-reduce:animate-none" />}
  </button>
}
