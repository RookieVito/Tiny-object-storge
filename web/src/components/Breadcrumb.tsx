interface BreadcrumbProps {
  prefix: string;
  onNavigate: (prefix: string) => void;
}

export default function Breadcrumb({ prefix, onNavigate }: BreadcrumbProps) {
  if (!prefix) return null;

  const parts = prefix.endsWith('/') ? prefix.slice(0, -1).split('/') : prefix.split('/');

  return (
    <nav className="mb-5 flex items-center gap-0.5 font-mono text-sm view-enter-fast">
      <button
        onClick={() => onNavigate('')}
        className="text-neon-cyan/70 hover:text-neon-cyan transition-colors duration-200"
      >
        /
      </button>
      {parts.map((part, i) => {
        const path = parts.slice(0, i + 1).join('/') + '/';
        const isLast = i === parts.length - 1;
        return (
          <span key={path} className="flex items-center gap-0.5">
            <span className="text-terminal-bright mx-0.5 select-none">/</span>
            {isLast ? (
              <span className="text-neon-cyan text-glow-cyan">{part}</span>
            ) : (
              <button
                onClick={() => onNavigate(path)}
                className="text-neon-cyan/60 hover:text-neon-cyan transition-colors duration-200"
              >
                {part}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}
