import { useNavigate } from 'react-router-dom';
import { EmptyState } from '../components/EmptyState';
import { Button } from '../components/Button';

export function NotFoundPage() {
  const navigate = useNavigate();
  return (
    <EmptyState
      title="Not found"
      description="That page doesn't exist. It may have been a stale link."
      action={
        <Button variant="primary" onClick={() => navigate('/')}>
          Back to projects
        </Button>
      }
    />
  );
}
