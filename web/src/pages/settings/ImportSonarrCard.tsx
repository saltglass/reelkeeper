import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

/** A pointer to the one-off Sonarr import, which isn't in the main menu. */
export function ImportSonarrCard() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Moving from Sonarr?</CardTitle>
        <CardDescription>
          Bring over what only Sonarr knows: which episodes it was monitoring (including older ones
          it never got), tags, history and the blocklist. Your files are found by scanning the
          folders above.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button asChild variant="outline">
          <Link to="/settings/import-sonarr">Import from Sonarr</Link>
        </Button>
      </CardContent>
    </Card>
  );
}
